// SPDX-License-Identifier: GPL-3.0-or-later

// Prometheus metrics collector for the breezy daemon. Each gauge is
// labelled with the device name and 16-byte device ID so a single
// dashboard can plot multiple ERVs at once. The collectors are owned
// by a Metrics struct and registered against a caller-supplied
// prometheus.Registerer (we pass prometheus.NewRegistry() in main so
// /metrics is hermetic — no Go runtime metrics, no collectors leaking
// in from imported libraries).
//
// Update(name, id, snap) translates a Snapshot into gauge values. It
// is invoked both by the poller's success path (through main's
// orchestration) AND lazily before each /metrics scrape, so a slow
// poll never serves stale numbers without at least a fresh
// breezy_last_poll_timestamp.
//
// Missing params are intentionally skipped — gauges retain their
// last-set value rather than being reset to zero. Stale values are
// signalled exclusively by breezy_last_poll_timestamp + breezy_up,
// which Prometheus operators are accustomed to alerting on.
package main

import (
	"fmt"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics owns all the Prometheus collectors exposed by breezyd. One
// instance is shared across all devices; per-device labels keep the
// time series distinct.
type Metrics struct {
	// Configured/state gauges.
	power                 *prometheus.GaugeVec
	airflowMode           *prometheus.GaugeVec
	speedMode             *prometheus.GaugeVec
	speedManualPct        *prometheus.GaugeVec
	heaterEnabled         *prometheus.GaugeVec
	humidityThreshold     *prometheus.GaugeVec
	co2Threshold          *prometheus.GaugeVec
	vocThreshold          *prometheus.GaugeVec
	humiditySensorEnabled *prometheus.GaugeVec
	co2SensorEnabled      *prometheus.GaugeVec
	vocSensorEnabled      *prometheus.GaugeVec
	filterTimeoutDays     *prometheus.GaugeVec

	// Live state.
	fanRPM                *prometheus.GaugeVec // labels: device,id,fan
	heaterRunning         *prometheus.GaugeVec
	inUserControl         *prometheus.GaugeVec
	specialMode           *prometheus.GaugeVec
	specialModeRemaining  *prometheus.GaugeVec
	sensorAlert           *prometheus.GaugeVec // labels: device,id,sensor
	recoveryEfficiency    *prometheus.GaugeVec
	frostProtectionActive *prometheus.GaugeVec

	// Sensors.
	humidityPct *prometheus.GaugeVec
	eco2PPM     *prometheus.GaugeVec
	vocIndex    *prometheus.GaugeVec
	temperature *prometheus.GaugeVec // labels: device,id,position

	// Service / health.
	filterStatus           *prometheus.GaugeVec
	filterRemainingSeconds *prometheus.GaugeVec
	motorLifetimeSeconds   *prometheus.GaugeVec
	rtcBatteryVolts        *prometheus.GaugeVec
	faultLevel             *prometheus.GaugeVec

	// Daemon health.
	lastPollTimestamp *prometheus.GaugeVec
	pollErrorsTotal   *prometheus.CounterVec // labels: device,id,kind
	up                *prometheus.GaugeVec
	info              *prometheus.GaugeVec // labels: device,id,firmware,build_date

	// Energy accounting (opt-in; only emitted for supported models).
	EnergyRecoveredWatts      *prometheus.GaugeVec
	EnergyConsumedWatts       *prometheus.GaugeVec
	EnergyHeatingTodayKWh     *prometheus.GaugeVec
	EnergyCoolingTodayKWh     *prometheus.GaugeVec
	EnergyConsumedTodayKWh    *prometheus.GaugeVec
	EnergyHeatingMonthKWh     *prometheus.GaugeVec
	EnergyCoolingMonthKWh     *prometheus.GaugeVec
	EnergyConsumedMonthKWh    *prometheus.GaugeVec
	EnergyHeatingLifetimeKWh  *prometheus.GaugeVec
	EnergyCoolingLifetimeKWh  *prometheus.GaugeVec
	EnergyConsumedLifetimeKWh *prometheus.GaugeVec
}

// labels common to every per-device metric.
var deviceLabels = []string{"device", "id"}

// gaugeDef describes one Prometheus gauge collector. The assign closure
// stores the constructed *GaugeVec into the right field on *Metrics so the
// field shape that callers depend on is unchanged.
type gaugeDef struct {
	name, help string
	labels     []string
	assign     func(m *Metrics, g *prometheus.GaugeVec)
}

// counterDef mirrors gaugeDef for *CounterVec collectors. Today there's
// just one (poll_errors_total); the shape matches gaugeDef for symmetry.
type counterDef struct {
	name, help string
	labels     []string
	assign     func(m *Metrics, c *prometheus.CounterVec)
}

// withExtra returns deviceLabels followed by extras. Built per-call so
// callers can't accidentally share the underlying slice.
func withExtra(extras ...string) []string {
	out := make([]string, 0, len(deviceLabels)+len(extras))
	out = append(out, deviceLabels...)
	out = append(out, extras...)
	return out
}

// NewMetrics constructs every collector and registers it with reg.
// reg may be nil for tests that don't care about scraping; in that
// case the metrics are usable but not registered.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{}

	gauges := []gaugeDef{
		// Configured / state.
		{"breezy_power", "Configured power state (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.power = g }},
		{"breezy_airflow_mode", "Configured airflow mode (0=ventilation, 1=regeneration, 2=supply, 3=extract).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.airflowMode = g }},
		{"breezy_speed_mode", "Configured speed mode (1-3 preset, 255=manual).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.speedMode = g }},
		{"breezy_speed_manual_pct", "Configured manual fan speed percentage (10-100).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.speedManualPct = g }},
		{"breezy_heater_enabled", "User toggle for the electric reheater (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.heaterEnabled = g }},
		{"breezy_humidity_threshold_pct", "Configured humidity threshold (RH%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humidityThreshold = g }},
		{"breezy_co2_threshold_ppm", "Configured CO2 threshold (ppm).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.co2Threshold = g }},
		{"breezy_voc_threshold_index", "Configured VOC index threshold.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocThreshold = g }},
		{"breezy_humidity_sensor_enabled", "Humidity sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humiditySensorEnabled = g }},
		{"breezy_co2_sensor_enabled", "CO2 sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.co2SensorEnabled = g }},
		{"breezy_voc_sensor_enabled", "VOC sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocSensorEnabled = g }},
		{"breezy_filter_timeout_days", "Filter replacement interval (days).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterTimeoutDays = g }},

		// Live state.
		{"breezy_fan_rpm", "Live fan RPM by position.", withExtra("fan"), func(m *Metrics, g *prometheus.GaugeVec) { m.fanRPM = g }},
		{"breezy_heater_running", "Heater is currently energized (0/1). Can be 1 even when heater_enabled=0 due to frost protection.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.heaterRunning = g }},
		{"breezy_in_user_control", "Device is doing what the user configured (1) or under sensor/timer override (0).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.inUserControl = g }},
		{"breezy_special_mode", "Active special mode (0=off, 1=night, 2=turbo).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.specialMode = g }},
		{"breezy_special_mode_remaining_seconds", "Seconds remaining in active special mode.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.specialModeRemaining = g }},
		{"breezy_sensor_alert", "Per-sensor over-threshold flag (0/1) decoded from 0x84.", withExtra("sensor"), func(m *Metrics, g *prometheus.GaugeVec) { m.sensorAlert = g }},
		{"breezy_recovery_efficiency_pct", "Heat-recovery efficiency (0-100%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.recoveryEfficiency = g }},
		{"breezy_frost_protection_active", "Frost protection currently active (0/1).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.frostProtectionActive = g }},

		// Sensors.
		{"breezy_humidity_percent", "Current room humidity (RH%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humidityPct = g }},
		{"breezy_eco2_ppm", "Indoor eCO2 (CO2-equivalent computed from VOC sensor) in ppm.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.eco2PPM = g }},
		{"breezy_voc_index", "Live VOC index (0-500).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocIndex = g }},
		{"breezy_temperature_celsius", "Air temperature in degrees Celsius by position.", withExtra("position"), func(m *Metrics, g *prometheus.GaugeVec) { m.temperature = g }},

		// Service / health.
		{"breezy_filter_status", "Filter status (0=clean, 1=soiled).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterStatus = g }},
		{"breezy_filter_remaining_seconds", "Filter-change countdown remaining in seconds.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterRemainingSeconds = g }},
		{"breezy_motor_lifetime_seconds", "Lifetime motor operation odometer in seconds.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.motorLifetimeSeconds = g }},
		{"breezy_rtc_battery_volts", "RTC backup battery voltage in volts.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.rtcBatteryVolts = g }},
		{"breezy_fault_level", "Top-level fault severity (0=none, 1=alarm, 2=warning).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.faultLevel = g }},

		// Daemon health.
		{"breezy_last_poll_timestamp", "Unix timestamp of the most recent poll attempt.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.lastPollTimestamp = g }},
		{"breezy_up", "1 if the most recent poll succeeded, else 0.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.up = g }},
		{"breezy_info", "Per-device build/firmware diagnostics (constant 1; data is in labels).", []string{"device", "id", "firmware", "build_date"}, func(m *Metrics, g *prometheus.GaugeVec) { m.info = g }},

		// Energy accounting (opt-in; "device"-only label).
		{"breezyd_energy_recovered_watts", "Instantaneous heat-transfer power across the HRV exchanger. Positive = heating recovered (winter), negative = cooling recovered (summer).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyRecoveredWatts = g }},
		{"breezyd_energy_consumed_watts", "Instantaneous electric draw of both fans combined (magnitude).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedWatts = g }},
		{"breezyd_energy_heating_today_kwh", "Heating energy recovered today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingTodayKWh = g }},
		{"breezyd_energy_cooling_today_kwh", "Cooling energy recovered today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingTodayKWh = g }},
		{"breezyd_energy_consumed_today_kwh", "Electric energy consumed by the fans today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedTodayKWh = g }},
		{"breezyd_energy_heating_month_kwh", "Heating energy recovered this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingMonthKWh = g }},
		{"breezyd_energy_cooling_month_kwh", "Cooling energy recovered this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingMonthKWh = g }},
		{"breezyd_energy_consumed_month_kwh", "Electric energy consumed by the fans this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedMonthKWh = g }},
		{"breezyd_energy_heating_lifetime_kwh", "Heating energy recovered cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingLifetimeKWh = g }},
		{"breezyd_energy_cooling_lifetime_kwh", "Cooling energy recovered cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingLifetimeKWh = g }},
		{"breezyd_energy_consumed_lifetime_kwh", "Electric energy consumed by the fans cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedLifetimeKWh = g }},
	}

	counters := []counterDef{
		{"breezy_poll_errors_total", "Total number of poll errors, by classification.", withExtra("kind"), func(m *Metrics, c *prometheus.CounterVec) { m.pollErrorsTotal = c }},
	}

	gaugeCollectors := make([]prometheus.Collector, 0, len(gauges))
	for _, d := range gauges {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: d.name, Help: d.help}, d.labels)
		d.assign(m, g)
		gaugeCollectors = append(gaugeCollectors, g)
	}
	counterCollectors := make([]prometheus.Collector, 0, len(counters))
	for _, d := range counters {
		c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: d.name, Help: d.help}, d.labels)
		d.assign(m, c)
		counterCollectors = append(counterCollectors, c)
	}

	if reg != nil {
		for _, c := range gaugeCollectors {
			reg.MustRegister(c)
		}
		for _, c := range counterCollectors {
			reg.MustRegister(c)
		}
	}
	return m
}

// Update writes every gauge derivable from snap. Missing params are
// silently skipped — see the package doc for why.
func (m *Metrics) Update(name, id string, snap Snapshot) {
	lbl := prometheus.Labels{"device": name, "id": id}

	// Daemon health: always set (these don't depend on individual params).
	if !snap.LastPoll.IsZero() {
		m.lastPollTimestamp.With(lbl).Set(float64(snap.LastPoll.Unix()))
	}
	if snap.LastErr != nil {
		m.up.With(lbl).Set(0)
	} else if !snap.LastPoll.IsZero() {
		m.up.With(lbl).Set(1)
	}

	// Configured/state.
	if v, ok := metricUint8(snap, 0x0001); ok {
		m.power.With(lbl).Set(boolish(v))
	}
	if v, ok := metricUint8(snap, 0x00B7); ok {
		m.airflowMode.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x0002); ok {
		m.speedMode.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x0044); ok {
		m.speedManualPct.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x0068); ok {
		m.heaterEnabled.With(lbl).Set(boolish(v))
	}
	if v, ok := metricUint8(snap, 0x0019); ok {
		m.humidityThreshold.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint16(snap, 0x001A); ok {
		m.co2Threshold.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint16(snap, 0x031F); ok {
		m.vocThreshold.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x000F); ok {
		m.humiditySensorEnabled.With(lbl).Set(boolish(v))
	}
	if v, ok := metricUint8(snap, 0x0011); ok {
		m.co2SensorEnabled.With(lbl).Set(boolish(v))
	}
	if v, ok := metricUint8(snap, 0x0315); ok {
		m.vocSensorEnabled.With(lbl).Set(boolish(v))
	}
	if v, ok := metricUint16(snap, 0x0063); ok {
		m.filterTimeoutDays.With(lbl).Set(float64(v))
	}

	// Live state.
	if v, ok := metricUint16(snap, 0x004A); ok {
		m.fanRPM.With(prometheus.Labels{"device": name, "id": id, "fan": "supply"}).Set(float64(v))
	}
	if v, ok := metricUint16(snap, 0x004B); ok {
		m.fanRPM.With(prometheus.Labels{"device": name, "id": id, "fan": "extract"}).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x0081); ok {
		m.heaterRunning.With(lbl).Set(boolish(v))
	}
	m.inUserControl.With(lbl).Set(map[bool]float64{true: 1, false: 0}[breezy.ComputeInUserControl(snap.Values)])
	if v, ok := metricUint8(snap, 0x0007); ok {
		m.specialMode.With(lbl).Set(float64(v))
	}
	if raw, ok := snap.Values[0x000B]; ok && len(raw) == 3 {
		// 3-byte time-of-day (sec, min, hr) — same encoding as in server.go.
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		m.specialModeRemaining.With(lbl).Set(float64(secs))
	}
	if raw, ok := snap.Values[0x0084]; ok && len(raw) >= 5 {
		setAlert := func(sensor string, b byte) {
			m.sensorAlert.With(prometheus.Labels{"device": name, "id": id, "sensor": sensor}).Set(boolish(b))
		}
		setAlert("humidity", raw[0])
		setAlert("co2", raw[1])
		setAlert("voc", raw[4])
	}
	if v, ok := metricUint8(snap, 0x0129); ok {
		m.recoveryEfficiency.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint8(snap, 0x030B); ok {
		m.frostProtectionActive.With(lbl).Set(boolish(v))
	}

	// Sensors.
	if v, ok := metricUint8(snap, 0x0025); ok {
		m.humidityPct.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint16(snap, 0x0027); ok {
		m.eco2PPM.With(lbl).Set(float64(v))
	}
	if v, ok := metricUint16(snap, 0x0320); ok {
		m.vocIndex.With(lbl).Set(float64(v))
	}
	for _, t := range []struct {
		id  breezy.ParamID
		pos string
	}{
		{0x001F, "outdoor"},
		{0x0020, "supply"},
		{0x0021, "exhaust_inlet"},
		{0x0022, "exhaust_outlet"},
	} {
		if v, ok := metricInt16(snap, t.id); ok {
			// Sentinels: -32768 / 32767 mean "not measured" in the firmware.
			if v == -32768 || v == 32767 {
				continue
			}
			m.temperature.With(prometheus.Labels{
				"device": name, "id": id, "position": t.pos,
			}).Set(float64(v) / 10.0)
		}
	}

	// Service.
	if v, ok := metricUint8(snap, 0x0088); ok {
		m.filterStatus.With(lbl).Set(boolish(v))
	}
	if raw, ok := snap.Values[0x0064]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		m.filterRemainingSeconds.With(lbl).Set(float64(secs))
	}
	if raw, ok := snap.Values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		m.motorLifetimeSeconds.With(lbl).Set(float64(secs))
	}
	if v, ok := metricUint16(snap, 0x0024); ok {
		m.rtcBatteryVolts.With(lbl).Set(float64(v) / 1000.0)
	}
	if v, ok := metricUint8(snap, 0x0083); ok {
		m.faultLevel.With(lbl).Set(float64(v))
	}

	// Firmware info: constant-1 gauge with labels for diagnostics.
	if raw, ok := snap.Values[0x0086]; ok && len(raw) == 6 {
		fw := fmt.Sprintf("%d.%02d", raw[0], raw[1])
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		bd := fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
		m.info.With(prometheus.Labels{
			"device": name, "id": id, "firmware": fw, "build_date": bd,
		}).Set(1)
	}
}

// SetEnergy updates all eight energy gauges for a device. When the
// tracker reports an unsupported model (Error != ""), previously-
// emitted samples for this device are dropped via DeleteLabelValues so
// /metrics doesn't expose phantom zeros for un-calibrated units.
func (m *Metrics) SetEnergy(device string, ev breezy.EnergyValues) {
	all := []*prometheus.GaugeVec{
		m.EnergyRecoveredWatts, m.EnergyConsumedWatts,
		m.EnergyHeatingTodayKWh, m.EnergyCoolingTodayKWh, m.EnergyConsumedTodayKWh,
		m.EnergyHeatingMonthKWh, m.EnergyCoolingMonthKWh, m.EnergyConsumedMonthKWh,
		m.EnergyHeatingLifetimeKWh, m.EnergyCoolingLifetimeKWh, m.EnergyConsumedLifetimeKWh,
	}
	if ev.Error != "" {
		for _, g := range all {
			g.DeleteLabelValues(device)
		}
		return
	}
	m.EnergyRecoveredWatts.WithLabelValues(device).Set(ev.InstantW)
	m.EnergyConsumedWatts.WithLabelValues(device).Set(ev.ConsumedW)
	m.EnergyHeatingTodayKWh.WithLabelValues(device).Set(ev.HeatingTodayKWh)
	m.EnergyCoolingTodayKWh.WithLabelValues(device).Set(ev.CoolingTodayKWh)
	m.EnergyConsumedTodayKWh.WithLabelValues(device).Set(ev.ConsumedTodayKWh)
	m.EnergyHeatingMonthKWh.WithLabelValues(device).Set(ev.HeatingMonthKWh)
	m.EnergyCoolingMonthKWh.WithLabelValues(device).Set(ev.CoolingMonthKWh)
	m.EnergyConsumedMonthKWh.WithLabelValues(device).Set(ev.ConsumedMonthKWh)
	m.EnergyHeatingLifetimeKWh.WithLabelValues(device).Set(ev.HeatingLifetimeKWh)
	m.EnergyCoolingLifetimeKWh.WithLabelValues(device).Set(ev.CoolingLifetimeKWh)
	m.EnergyConsumedLifetimeKWh.WithLabelValues(device).Set(ev.ConsumedLifetimeKWh)
}

// RecordPollError increments breezy_poll_errors_total. Wire from the
// poller's OnError so the counter accumulates regardless of whether
// /metrics is being scraped.
func (m *Metrics) RecordPollError(name, id, kind string) {
	m.pollErrorsTotal.With(prometheus.Labels{
		"device": name, "id": id, "kind": kind,
	}).Inc()
}

// boolish maps any non-zero byte to 1.0 and zero to 0.0. The firmware
// uses 0/1/2 for several toggles where 2 means "invert" — for metrics
// we treat any non-zero as "on", which matches user intent.
func boolish(b byte) float64 {
	if b == 0 {
		return 0
	}
	return 1
}

// metricUint8 / metricUint16 / metricInt16 are thin aliases over the
// shared decode helpers in pkg/breezy, kept here so the metrics call
// sites read consistently. The aliases let any future "treat the
// metric pipeline differently" tweak land in one place.
func metricUint8(snap Snapshot, id breezy.ParamID) (uint8, bool) {
	return breezy.Uint8At(snap.Values, id)
}
func metricUint16(snap Snapshot, id breezy.ParamID) (uint16, bool) {
	return breezy.Uint16At(snap.Values, id)
}
func metricInt16(snap Snapshot, id breezy.ParamID) (int16, bool) {
	return breezy.Int16At(snap.Values, id)
}

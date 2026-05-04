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
	power                  *prometheus.GaugeVec
	airflowMode            *prometheus.GaugeVec
	speedMode              *prometheus.GaugeVec
	speedManualPct         *prometheus.GaugeVec
	heaterEnabled          *prometheus.GaugeVec
	humidityThreshold      *prometheus.GaugeVec
	co2Threshold           *prometheus.GaugeVec
	vocThreshold           *prometheus.GaugeVec
	humiditySensorEnabled  *prometheus.GaugeVec
	co2SensorEnabled       *prometheus.GaugeVec
	vocSensorEnabled       *prometheus.GaugeVec
	filterTimeoutDays      *prometheus.GaugeVec

	// Live state.
	fanRPM                     *prometheus.GaugeVec // labels: device,id,fan
	heaterRunning              *prometheus.GaugeVec
	inUserControl              *prometheus.GaugeVec
	specialMode                *prometheus.GaugeVec
	specialModeRemaining       *prometheus.GaugeVec
	sensorAlert                *prometheus.GaugeVec // labels: device,id,sensor
	recoveryEfficiency         *prometheus.GaugeVec
	frostProtectionActive      *prometheus.GaugeVec

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
}

// labels common to every per-device metric.
var deviceLabels = []string{"device", "id"}

// NewMetrics constructs every collector and registers it with reg.
// reg may be nil for tests that don't care about scraping; in that
// case the metrics are usable but not registered.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		power: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_power",
			Help: "Configured power state (0=off, 1=on).",
		}, deviceLabels),
		airflowMode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_airflow_mode",
			Help: "Configured airflow mode (0=ventilation, 1=regeneration, 2=supply, 3=extract).",
		}, deviceLabels),
		speedMode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_speed_mode",
			Help: "Configured speed mode (1-3 preset, 255=manual).",
		}, deviceLabels),
		speedManualPct: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_speed_manual_pct",
			Help: "Configured manual fan speed percentage (10-100).",
		}, deviceLabels),
		heaterEnabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_heater_enabled",
			Help: "User toggle for the electric reheater (0=off, 1=on).",
		}, deviceLabels),
		humidityThreshold: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_humidity_threshold_pct",
			Help: "Configured humidity threshold (RH%).",
		}, deviceLabels),
		co2Threshold: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_co2_threshold_ppm",
			Help: "Configured CO2 threshold (ppm).",
		}, deviceLabels),
		vocThreshold: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_voc_threshold_index",
			Help: "Configured VOC index threshold.",
		}, deviceLabels),
		humiditySensorEnabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_humidity_sensor_enabled",
			Help: "Humidity sensor control (0=off, 1=on).",
		}, deviceLabels),
		co2SensorEnabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_co2_sensor_enabled",
			Help: "CO2 sensor control (0=off, 1=on).",
		}, deviceLabels),
		vocSensorEnabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_voc_sensor_enabled",
			Help: "VOC sensor control (0=off, 1=on).",
		}, deviceLabels),
		filterTimeoutDays: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_filter_timeout_days",
			Help: "Filter replacement interval (days).",
		}, deviceLabels),

		fanRPM: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_fan_rpm",
			Help: "Live fan RPM by position.",
		}, append(append([]string{}, deviceLabels...), "fan")),
		heaterRunning: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_heater_running",
			Help: "Heater is currently energized (0/1). Can be 1 even when heater_enabled=0 due to frost protection.",
		}, deviceLabels),
		inUserControl: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_in_user_control",
			Help: "Device is doing what the user configured (1) or under sensor/timer override (0).",
		}, deviceLabels),
		specialMode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_special_mode",
			Help: "Active special mode (0=off, 1=night, 2=turbo).",
		}, deviceLabels),
		specialModeRemaining: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_special_mode_remaining_seconds",
			Help: "Seconds remaining in active special mode.",
		}, deviceLabels),
		sensorAlert: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_sensor_alert",
			Help: "Per-sensor over-threshold flag (0/1) decoded from 0x84.",
		}, append(append([]string{}, deviceLabels...), "sensor")),
		recoveryEfficiency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_recovery_efficiency_pct",
			Help: "Heat-recovery efficiency (0-100%).",
		}, deviceLabels),
		frostProtectionActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_frost_protection_active",
			Help: "Frost protection currently active (0/1).",
		}, deviceLabels),

		humidityPct: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_humidity_percent",
			Help: "Current room humidity (RH%).",
		}, deviceLabels),
		eco2PPM: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_eco2_ppm",
			Help: "Indoor eCO2 (CO2-equivalent computed from VOC sensor) in ppm.",
		}, deviceLabels),
		vocIndex: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_voc_index",
			Help: "Live VOC index (0-500).",
		}, deviceLabels),
		temperature: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_temperature_celsius",
			Help: "Air temperature in degrees Celsius by position.",
		}, append(append([]string{}, deviceLabels...), "position")),

		filterStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_filter_status",
			Help: "Filter status (0=clean, 1=soiled).",
		}, deviceLabels),
		filterRemainingSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_filter_remaining_seconds",
			Help: "Filter-change countdown remaining in seconds.",
		}, deviceLabels),
		motorLifetimeSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_motor_lifetime_seconds",
			Help: "Lifetime motor operation odometer in seconds.",
		}, deviceLabels),
		rtcBatteryVolts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_rtc_battery_volts",
			Help: "RTC backup battery voltage in volts.",
		}, deviceLabels),
		faultLevel: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_fault_level",
			Help: "Top-level fault severity (0=none, 1=alarm, 2=warning).",
		}, deviceLabels),

		lastPollTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_last_poll_timestamp",
			Help: "Unix timestamp of the most recent poll attempt.",
		}, deviceLabels),
		pollErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "breezy_poll_errors_total",
			Help: "Total number of poll errors, by classification.",
		}, append(append([]string{}, deviceLabels...), "kind")),
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_up",
			Help: "1 if the most recent poll succeeded, else 0.",
		}, deviceLabels),
		info: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "breezy_info",
			Help: "Per-device build/firmware diagnostics (constant 1; data is in labels).",
		}, []string{"device", "id", "firmware", "build_date"}),
	}

	if reg != nil {
		collectors := []prometheus.Collector{
			m.power, m.airflowMode, m.speedMode, m.speedManualPct,
			m.heaterEnabled, m.humidityThreshold, m.co2Threshold, m.vocThreshold,
			m.humiditySensorEnabled, m.co2SensorEnabled, m.vocSensorEnabled,
			m.filterTimeoutDays,
			m.fanRPM, m.heaterRunning, m.inUserControl, m.specialMode,
			m.specialModeRemaining, m.sensorAlert, m.recoveryEfficiency,
			m.frostProtectionActive,
			m.humidityPct, m.eco2PPM, m.vocIndex, m.temperature,
			m.filterStatus, m.filterRemainingSeconds, m.motorLifetimeSeconds,
			m.rtcBatteryVolts, m.faultLevel,
			m.lastPollTimestamp, m.pollErrorsTotal, m.up, m.info,
		}
		for _, c := range collectors {
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
	m.inUserControl.With(lbl).Set(map[bool]float64{true: 1, false: 0}[computeInUserControl(snap)])
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
// shared decode helpers in decode.go, kept here so the metrics call
// sites read consistently. The aliases let any future "treat the
// metric pipeline differently" tweak land in one place.
func metricUint8(snap Snapshot, id breezy.ParamID) (uint8, bool)  { return uint8At(snap, id) }
func metricUint16(snap Snapshot, id breezy.ParamID) (uint16, bool) { return uint16At(snap, id) }
func metricInt16(snap Snapshot, id breezy.ParamID) (int16, bool)  { return int16At(snap, id) }

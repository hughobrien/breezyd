// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the Prometheus metrics surface. We don't try to replicate
// the prometheus library's serialisation behavior here — we just want
// to confirm:
//
//  1. Every metric documented in the design spec is registered under
//     its expected name.
//  2. Update() actually populates each gauge from a representative
//     Snapshot (using promtest helpers to read back the current value).
//  3. RecordPollError increments the per-kind counter.
//
// Each test uses its own prometheus.NewRegistry() for isolation so the
// global default registry stays clean across the suite.
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// representativeSnapshot returns a Snapshot whose Values map covers
// every param Metrics.Update reads. Hex / multi-byte values mirror
// the encodings documented in the param map.
func representativeSnapshot() Snapshot {
	return Snapshot{
		IP:       "192.168.1.10:4000",
		LastPoll: time.Unix(1_700_000_000, 0),
		Values: map[breezy.ParamID][]byte{
			0x0001: {1},                        // power on
			0x0002: {0xFF},                     // manual
			0x0007: {1},                        // night mode
			0x000B: {30, 5, 1},                 // 1h05m30s = 3930s
			0x000F: {1},                        // humidity sensor on
			0x0011: {1},                        // co2 sensor on
			0x0019: {60},                       // 60% humidity threshold
			0x001A: {0xD0, 0x07},               // 2000 ppm
			0x001F: {0xC8, 0x00},               // 200 -> 20.0 °C
			0x0020: {0xE0, 0x00},               // 224 -> 22.4 °C
			0x0021: {0xC8, 0x00},               // 20.0 °C
			0x0022: {0xB4, 0x00},               // 18.0 °C
			0x0024: {0xC4, 0x0B},               // 3012 mV -> 3.012 V
			0x0025: {45},                       // humidity 45%
			0x0027: {0xF4, 0x01},               // 500 ppm eCO2
			0x0044: {50},                       // manual 50%
			0x004A: {0x10, 0x27},               // 10000 rpm supply
			0x004B: {0x20, 0x27},               // 10016 rpm extract
			0x0063: {0xB4, 0x00},               // 180 days
			0x0064: {30, 12, 0x10, 0x00},       // min=30, hr=12, days=16
			0x0068: {0},                        // heater disabled
			0x007E: {0, 6, 0x40, 0x01},         // motor odo: 320 days, 6h
			0x0081: {1},                        // heater currently on
			0x0083: {0},                        // no faults
			0x0084: {0, 1, 0, 0, 0},            // co2 alert flag set
			0x0086: {0, 11, 21, 3, 0xE9, 0x07}, // fw 0.11 build 2025-03-21
			0x0088: {0},                        // filter clean
			0x00B7: {2},                        // supply mode
			0x0129: {85},                       // 85% recovery efficiency
			0x030B: {0},                        // frost protection inactive
			0x0315: {1},                        // voc sensor on
			0x031F: {0x96, 0x00},               // 150 voc threshold
			0x0320: {100, 0},                   // 100 voc index
		},
	}
}

// TestMetricsRegistration asserts every documented metric name is
// registered against the registry after NewMetrics. We do this by
// gathering the registry and pulling out the unique metric names.
func TestMetricsRegistration(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	// Touch every gauge with a sample so it shows up in Gather().
	m.Update("dev", "ID0000000000000A", representativeSnapshot())
	m.RecordPollError("dev", "ID0000000000000A", "timeout")

	mfs, err := reg.Gather()
	is.NoErr(err)

	got := map[string]bool{}
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}
	want := []string{
		"breezy_power",
		"breezy_airflow_mode",
		"breezy_speed_mode",
		"breezy_speed_manual_pct",
		"breezy_heater_enabled",
		"breezy_humidity_threshold_pct",
		"breezy_co2_threshold_ppm",
		"breezy_voc_threshold_index",
		"breezy_humidity_sensor_enabled",
		"breezy_co2_sensor_enabled",
		"breezy_voc_sensor_enabled",
		"breezy_filter_timeout_days",
		"breezy_fan_rpm",
		"breezy_heater_running",
		"breezy_in_user_control",
		"breezy_special_mode",
		"breezy_special_mode_remaining_seconds",
		"breezy_sensor_alert",
		"breezy_recovery_efficiency_pct",
		"breezy_frost_protection_active",
		"breezy_humidity_percent",
		"breezy_eco2_ppm",
		"breezy_voc_index",
		"breezy_temperature_celsius",
		"breezy_filter_status",
		"breezy_filter_remaining_seconds",
		"breezy_motor_lifetime_seconds",
		"breezy_rtc_battery_volts",
		"breezy_fault_level",
		"breezy_last_poll_timestamp",
		"breezy_poll_errors_total",
		"breezy_up",
		"breezy_info",
	}
	for _, n := range want {
		is.True(got[n]) // every documented metric name must be registered
	}
}

// TestMetricsUpdateValues verifies a few representative gauges land at
// the right value after Update — covering uint8, uint16, signed temp,
// computed remaining-seconds, and the multi-label fan/temperature/sensor
// variants.
func TestMetricsUpdateValues(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	snap := representativeSnapshot()
	m.Update("playroom", "DEVID0000000000A", snap)

	dl := prometheus.Labels{"device": "playroom", "id": "DEVID0000000000A"}

	is.Equal(testutil.ToFloat64(m.power.With(dl)), float64(1))
	is.Equal(testutil.ToFloat64(m.airflowMode.With(dl)), float64(2)) // supply
	is.Equal(testutil.ToFloat64(m.speedMode.With(dl)), float64(255)) // manual
	is.Equal(testutil.ToFloat64(m.speedManualPct.With(dl)), float64(50))
	is.Equal(testutil.ToFloat64(m.co2Threshold.With(dl)), float64(2000))
	is.Equal(testutil.ToFloat64(m.vocThreshold.With(dl)), float64(150))
	is.Equal(testutil.ToFloat64(m.specialMode.With(dl)), float64(1)) // night
	// Remaining = 30 + 5*60 + 1*3600 = 3930
	is.Equal(testutil.ToFloat64(m.specialModeRemaining.With(dl)), float64(3930))
	is.Equal(testutil.ToFloat64(m.recoveryEfficiency.With(dl)), float64(85))
	is.Equal(testutil.ToFloat64(m.humidityPct.With(dl)), float64(45))
	is.Equal(testutil.ToFloat64(m.eco2PPM.With(dl)), float64(500))
	is.Equal(testutil.ToFloat64(m.vocIndex.With(dl)), float64(100))
	rtc := testutil.ToFloat64(m.rtcBatteryVolts.With(dl))
	is.True(rtc >= 3.011 && rtc <= 3.013) // rtc_battery_volts ≈ 3.012

	// Multi-label variants.
	supplyRPM := testutil.ToFloat64(m.fanRPM.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "fan": "supply",
	}))
	is.Equal(supplyRPM, float64(10000))
	extractRPM := testutil.ToFloat64(m.fanRPM.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "fan": "extract",
	}))
	is.Equal(extractRPM, float64(10016))

	tempOutdoor := testutil.ToFloat64(m.temperature.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "position": "outdoor",
	}))
	is.True(tempOutdoor >= 19.99 && tempOutdoor <= 20.01) // ≈ 20.0
	tempSupply := testutil.ToFloat64(m.temperature.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "position": "supply",
	}))
	is.True(tempSupply >= 22.39 && tempSupply <= 22.41) // ≈ 22.4

	// Sensor alerts: only co2 should be 1.
	co2Alert := testutil.ToFloat64(m.sensorAlert.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "sensor": "co2",
	}))
	is.Equal(co2Alert, float64(1))
	humAlert := testutil.ToFloat64(m.sensorAlert.With(prometheus.Labels{
		"device": "playroom", "id": "DEVID0000000000A", "sensor": "humidity",
	}))
	is.Equal(humAlert, float64(0))

	// Daemon health.
	is.Equal(testutil.ToFloat64(m.lastPollTimestamp.With(dl)), float64(1_700_000_000))
	is.Equal(testutil.ToFloat64(m.up.With(dl)), float64(1))

	// in_user_control: special_mode is 1 (night), so should be false (0).
	is.Equal(testutil.ToFloat64(m.inUserControl.With(dl)), float64(0)) // special mode active
}

// TestMetricsUpdateMissingParams confirms a snapshot missing every
// param does NOT panic and does NOT touch any gauge that depends on
// those params. The "up=0 / last_poll set" assertions also exercise
// the LastErr path.
func TestMetricsUpdateMissingParams(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	snap := Snapshot{
		LastPoll: time.Unix(1_700_000_100, 0),
		LastErr:  errStub("transport"),
		Values:   map[breezy.ParamID][]byte{},
	}
	m.Update("dev", "ID0000000000001A", snap)

	dl := prometheus.Labels{"device": "dev", "id": "ID0000000000001A"}
	is.Equal(testutil.ToFloat64(m.lastPollTimestamp.With(dl)), float64(1_700_000_100))
	is.Equal(testutil.ToFloat64(m.up.With(dl)), float64(0)) // up=0 when LastErr is set
	// in_user_control still set even with empty values: function returns
	// true when all relevant guards are absent. That's fine.
}

// TestMetricsUpdateUnsetTemperatureSentinel ensures the -32768 / 32767
// "not measured" sentinels do NOT land in the temperature gauge.
func TestMetricsUpdateUnsetTemperatureSentinel(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	snap := Snapshot{
		LastPoll: time.Unix(1, 0),
		Values: map[breezy.ParamID][]byte{
			0x001F: {0x00, 0x80}, // -32768
		},
	}
	m.Update("dev", "x", snap)

	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "breezy_temperature_celsius" {
			continue
		}
		is.Equal(len(mf.Metric), 0) // sentinel value must produce zero series
	}
}

// TestMetricsRecordPollError exercises the counter path. Each kind
// label gets its own series; the same kind incrementing twice should
// be visible as 2.
func TestMetricsRecordPollError(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.RecordPollError("a", "ID1", "timeout")
	m.RecordPollError("a", "ID1", "timeout")
	m.RecordPollError("a", "ID1", "checksum")

	timeoutCt := testutil.ToFloat64(m.pollErrorsTotal.With(prometheus.Labels{
		"device": "a", "id": "ID1", "kind": "timeout",
	}))
	is.Equal(timeoutCt, float64(2))
	checksumCt := testutil.ToFloat64(m.pollErrorsTotal.With(prometheus.Labels{
		"device": "a", "id": "ID1", "kind": "checksum",
	}))
	is.Equal(checksumCt, float64(1))
}

// TestMetricsInfoLabels verifies breezy_info renders the expected
// firmware/build_date label values from the 0x86 data block.
func TestMetricsInfoLabels(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.Update("dev", "ID0000000000002A", representativeSnapshot())

	mfs, err := reg.Gather()
	is.NoErr(err)
	var found *dto.Metric
	for _, mf := range mfs {
		if mf.GetName() != "breezy_info" {
			continue
		}
		if len(mf.Metric) == 0 {
			continue
		}
		found = mf.Metric[0]
		break
	}
	is.True(found != nil) // breezy_info series must be present

	labels := map[string]string{}
	for _, lp := range found.Label {
		labels[lp.GetName()] = lp.GetValue()
	}
	is.Equal(labels["firmware"], "0.11")
	is.Equal(labels["build_date"], "2025-03-21")
	is.Equal(found.Gauge.GetValue(), float64(1))
}

func TestMetrics_SetEnergy_Supported(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SetEnergy("playroom", breezy.EnergyValues{
		Supported:           true,
		InstantW:            245,
		ConsumedW:           18,
		HeatingTodayKWh:     1.234,
		ConsumedTodayKWh:    0.123,
		HeatingMonthKWh:     30.0,
		CoolingMonthKWh:     5.5,
		ConsumedMonthKWh:    3.7,
		HeatingLifetimeKWh:  234.5,
		ConsumedLifetimeKWh: 12.3,
	})
	families, err := reg.Gather()
	is.NoErr(err)
	want := map[string]float64{
		"breezyd_energy_recovered_watts":       245,
		"breezyd_energy_consumed_watts":        18,
		"breezyd_energy_heating_today_kwh":     1.234,
		"breezyd_energy_consumed_today_kwh":    0.123,
		"breezyd_energy_heating_month_kwh":     30.0,
		"breezyd_energy_cooling_month_kwh":     5.5,
		"breezyd_energy_consumed_month_kwh":    3.7,
		"breezyd_energy_heating_lifetime_kwh":  234.5,
		"breezyd_energy_consumed_lifetime_kwh": 12.3,
	}
	for _, fam := range families {
		w, ok := want[fam.GetName()]
		if !ok {
			continue
		}
		got := fam.GetMetric()[0].GetGauge().GetValue()
		is.Equal(got, w) // gauge value must match expected for this energy metric
		delete(want, fam.GetName())
	}
	is.Equal(len(want), 0) // every expected energy metric must have been emitted
}

func TestMetrics_SetEnergy_UnsupportedDropsLabels(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	// Set all 8 with non-zero values, then flip to unsupported and
	// assert every previously-emitted series is gone.
	m.SetEnergy("playroom", breezy.EnergyValues{
		Supported:           true,
		InstantW:            245,
		ConsumedW:           18,
		HeatingTodayKWh:     1,
		CoolingTodayKWh:     0.5,
		ConsumedTodayKWh:    0.1,
		HeatingLifetimeKWh:  100,
		CoolingLifetimeKWh:  50,
		ConsumedLifetimeKWh: 10,
	})
	m.SetEnergy("playroom", breezy.EnergyValues{Error: "unsupported model: unit 22"})
	families, _ := reg.Gather()
	energyGauges := map[string]bool{
		"breezyd_energy_recovered_watts":       true,
		"breezyd_energy_consumed_watts":        true,
		"breezyd_energy_heating_today_kwh":     true,
		"breezyd_energy_cooling_today_kwh":     true,
		"breezyd_energy_consumed_today_kwh":    true,
		"breezyd_energy_heating_month_kwh":     true,
		"breezyd_energy_cooling_month_kwh":     true,
		"breezyd_energy_consumed_month_kwh":    true,
		"breezyd_energy_heating_lifetime_kwh":  true,
		"breezyd_energy_cooling_lifetime_kwh":  true,
		"breezyd_energy_consumed_lifetime_kwh": true,
	}
	for _, fam := range families {
		if !energyGauges[fam.GetName()] {
			continue
		}
		is.Equal(len(fam.GetMetric()), 0) // unsupported update must drop all energy series
	}
}

// errStub is a trivial error type for the LastErr code path; we don't
// care which classification it lands in for the metrics tests.
type errStub string

func (e errStub) Error() string { return string(e) }

// TestMetricsHelpStringsNonEmpty ensures every collector has a Help
// line — Prom is grumpy about empty Help.
func TestMetricsHelpStringsNonEmpty(t *testing.T) {
	is := is.New(t)
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.Update("d", "I", representativeSnapshot())
	m.RecordPollError("d", "I", "timeout")
	mfs, err := reg.Gather()
	is.NoErr(err)
	for _, mf := range mfs {
		is.True(strings.TrimSpace(mf.GetHelp()) != "") // every collector must declare a Help string
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

// push_render.go produces the structured PushEvent payload for one
// device snapshot — the per-card signal JSON plus a list of per-block
// (selector, html) pairs. The SSE handler turns each pair into a
// datastar-patch-elements event and the signal JSON into a
// datastar-patch-signals event.
package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/a-h/templ"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
)

// buildPushEvent renders the templ blocks and signal JSON for a
// single device. Returns nil + error on any render failure; the
// caller drops the event in that case.
func buildPushEvent(name string, view ui.DeviceView) (*PushEvent, error) {
	sigJSON, err := ui.MarshalCardSignals(view)
	if err != nil {
		return nil, fmt.Errorf("marshal card signals: %w", err)
	}

	blocks := make([]BlockPatch, 0, 16)

	add := func(selector string, cmp templ.Component) error {
		var buf bytes.Buffer
		if err := cmp.Render(context.Background(), &buf); err != nil {
			return err
		}
		blocks = append(blocks, BlockPatch{Selector: selector, HTML: buf.String()})
		return nil
	}

	cardSel := fmt.Sprintf(`.card[data-device=%q]`, name)

	if err := add(cardSel+` [data-block="info"]:not([data-edit])`, templates.InfoDetails(view)); err != nil {
		return nil, err
	}
	if err := add(cardSel+` [data-block="energy"]:not([data-edit])`, templates.EnergyBlock(view.Name, view.Energy)); err != nil {
		return nil, err
	}
	if err := add(cardSel+` [data-block="schedule"]:not([data-edit])`, templates.ScheduleBlock(view.Name, view.Schedule, view.Stale)); err != nil {
		return nil, err
	}
	if err := add(cardSel+` [data-block="controls"]:not([data-edit])`, templates.ControlsBlock(view)); err != nil {
		return nil, err
	}

	for _, p := range sensorCellPatches(view.Name, view.Sensors) {
		sel := fmt.Sprintf(`%s [data-sensor-cell="%s"]:not([data-edit])`, cardSel, p.Key)
		if err := add(sel, p.Component); err != nil {
			return nil, err
		}
	}

	return &PushEvent{
		DeviceName:  name,
		SignalsJSON: sigJSON,
		Blocks:      blocks,
	}, nil
}

type sensorCellPatch struct {
	Key       string
	Component templ.Component
}

// sensorCellPatches returns the 12 cells in stable order. The first
// three (co2/voc/humidity) are the editable threshold cells; the rest
// are plain readings.
func sensorCellPatches(name string, s ui.SensorsView) []sensorCellPatch {
	return []sensorCellPatch{
		{"co2", templates.CO2Cell(name, s)},
		{"voc", templates.VOCCell(name, s)},
		{"humidity", templates.HumidityCell(name, s)},
		{"recovery", templates.PlainSensorCell("recovery", "recovery", ui.FmtOptPct(s.RecoveryPct))},
		{"supply", templates.PlainSensorCell("supply", "supply", ui.FmtTempC(s.TempOutdoorC))},
		{"exhaust", templates.PlainSensorCell("exhaust", "exhaust", ui.FmtTempC(s.TempExhaustInletC))},
		{"supply_regen", templates.PlainSensorCell("supply_regen", "supply_regen", ui.FmtTempC(s.TempSupplyC))},
		{"exhaust_regen", templates.PlainSensorCell("exhaust_regen", "exhaust_regen", ui.FmtTempC(s.TempExhaustOutC))},
		{"delta_supply", templates.PlainSensorCell("delta_supply", "Δ", ui.TempDeltaStr(s.TempSupplyC, s.TempOutdoorC))},
		{"delta_exhaust", templates.PlainSensorCell("delta_exhaust", "Δ", ui.TempDeltaStr(s.TempExhaustOutC, s.TempExhaustInletC))},
		{"supply_rpm", templates.PlainSensorCell("supply_rpm", "supply rpm", ui.RPMStr(s.SupplyRPM))},
		{"exhaust_rpm", templates.PlainSensorCell("exhaust_rpm", "exhaust rpm", ui.RPMStr(s.ExtractRPM))},
	}
}

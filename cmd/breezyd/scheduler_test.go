// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestScheduleTime_ParseAndString(t *testing.T) {
	cases := []struct {
		in   string
		want ScheduleTime
	}{
		{"00:00", 0},
		{"08:00", 480},
		{"22:30", 22*60 + 30},
		{"23:59", 1439},
	}
	for _, c := range cases {
		got, err := ParseScheduleTime(c.in)
		if err != nil {
			t.Errorf("ParseScheduleTime(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseScheduleTime(%q) = %d, want %d", c.in, got, c.want)
		}
		if back := got.String(); back != c.in {
			t.Errorf("%d.String() = %q, want %q", got, back, c.in)
		}
	}
	bad := []string{"", "8:00", "08:0", "24:00", "08:60", "08-00", "abc", "08:00:00"}
	for _, in := range bad {
		if _, err := ParseScheduleTime(in); err == nil {
			t.Errorf("ParseScheduleTime(%q): want error, got nil", in)
		}
	}
}

func TestScheduler_Validation(t *testing.T) {
	s := &Scheduler{}
	good := []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	if err := s.validate(good); err != nil {
		t.Errorf("good schedule rejected: %v", err)
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("empty disabled schedule rejected: %v", err)
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("empty enabled schedule rejected: %v", err)
	}

	type badCase struct {
		name    string
		entries []ScheduleEntry
	}
	bads := []badCase{
		{"unknown action", []ScheduleEntry{{At: 480, Action: "boost", Pct: 60}}},
		{"pct too low", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 5}}},
		{"pct too high", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 101}}},
		{"pct zero", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 0}}},
		{"duplicate at", []ScheduleEntry{
			{At: 600, Action: "regeneration", Pct: 60},
			{At: 600, Action: "off", Pct: 60},
		}},
	}
	for _, b := range bads {
		err := s.validate(b.entries)
		if err == nil {
			t.Errorf("%s: want error, got nil", b.name)
			continue
		}
		if !errors.Is(err, breezy.ErrInvalidArg) {
			t.Errorf("%s: want ErrInvalidArg, got %v", b.name, err)
		}
	}

	too := make([]ScheduleEntry, 25)
	for i := range too {
		too[i] = ScheduleEntry{At: ScheduleTime(i), Action: "regeneration", Pct: 60}
	}
	if err := s.validate(too); !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("25 entries: want ErrInvalidArg, got %v", err)
	}
}

func TestScheduleEntry_JSON(t *testing.T) {
	in := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	data, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"at":"08:00","action":"regeneration","pct":60}`
	if string(data) != want {
		t.Errorf("marshal: got %s, want %s", data, want)
	}
	var out ScheduleEntry
	if err := out.UnmarshalJSON([]byte(want)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip: %+v != %+v", out, in)
	}
}

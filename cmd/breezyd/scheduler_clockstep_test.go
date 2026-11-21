// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"testing"
	"time"

	"github.com/matryer/is"
)

// fireLog is one observed fire: the wall-clock (in the scheduler's zone)
// at which it happened and the entry's At-time.
type fireLog struct {
	When string
	At   ScheduleTime
}

// tickAndLog ticks s at now and appends to log if a fire happened.
func tickAndLog(s *Scheduler, fc *schedFakeClient, now time.Time, log *[]fireLog) {
	before := fc.writeCount()
	s.tick(context.Background(), now)
	if fc.writeCount() > before {
		s.mu.Lock()
		at := s.lastApply.At
		s.mu.Unlock()
		*log = append(*log, fireLog{When: now.Format("01-02 15:04 MST"), At: at})
	}
}

// TestScheduler_FallBackDay_EntriesOutsideRepeatedHour is the #257
// regression: entries outside the repeated hour must fire exactly once
// each at their own time across the Europe/Dublin fall-back day, with no
// spurious re-fire at the transition and no suppressed evening fire.
func TestScheduler_FallBackDay_EntriesOutsideRepeatedHour(t *testing.T) {
	is := is.New(t)
	dub, err := time.LoadLocation("Europe/Dublin")
	is.NoErr(err)

	s, fc := newSchedTest(t)
	is.NoErr(s.Replace(true, []ScheduleEntry{
		{At: 420, Action: "regeneration", Pct: 50}, // 07:00
		{At: 1320, Action: "off"},                  // 22:00
	}))

	var got []fireLog
	// 2026-10-24 21:59 IST (20:59 UTC) .. 2026-10-25 22:01 GMT (22:01 UTC), one UTC minute per tick.
	for u := atUTC(2026, 10, 24, 20, 59); !u.After(atUTC(2026, 10, 25, 22, 1)); u = u.Add(time.Minute) {
		tickAndLog(s, fc, u.In(dub), &got)
	}
	is.Equal(got, []fireLog{
		{"10-24 22:00 IST", 1320},
		{"10-25 07:00 GMT", 420},
		{"10-25 22:00 GMT", 1320},
	})
}

// TestScheduler_BackwardNTPStep: a wall clock stepped back a few minutes
// must neither re-fire anything nor suppress the entry's later real fire.
func TestScheduler_BackwardNTPStep(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	is.NoErr(s.Replace(true, []ScheduleEntry{
		{At: 420, Action: "regeneration", Pct: 50}, // 07:00
		{At: 1320, Action: "off"},                  // 22:00
	}))

	var got []fireLog
	d := func(day, h, m int) time.Time { return atUTC(2026, 5, day, h, m) }
	// Evening before: fire 22:00 on 05-05 so firedAt holds yesterday.
	tickAndLog(s, fc, d(5, 21, 59), &got)
	tickAndLog(s, fc, d(5, 22, 0), &got)
	// Next day: 07:00 fires, continue to 10:00, then step back 3 minutes.
	for u := d(6, 6, 59); !u.After(d(6, 10, 0)); u = u.Add(time.Minute) {
		tickAndLog(s, fc, u, &got)
	}
	tickAndLog(s, fc, d(6, 9, 57), &got) // NTP step back
	for u := d(6, 9, 58); !u.After(d(6, 22, 1)); u = u.Add(time.Minute) {
		tickAndLog(s, fc, u, &got)
	}
	is.Equal(got, []fireLog{
		{"05-05 22:00 UTC", 1320},
		{"05-06 07:00 UTC", 420},
		{"05-06 22:00 UTC", 1320},
	})
}

// TestScheduler_LordHoweFallBack: a 30-minute fall-back (Lord Howe,
// 02:00 LHDT -> 01:30 LHST) must not re-fire an entry outside the
// repeated half hour, and an entry inside it fires once.
func TestScheduler_LordHoweFallBack(t *testing.T) {
	is := is.New(t)
	lh, err := time.LoadLocation("Australia/Lord_Howe")
	is.NoErr(err)

	s, fc := newSchedTest(t)
	is.NoErr(s.Replace(true, []ScheduleEntry{
		{At: 60, Action: "regeneration", Pct: 40},  // 01:00
		{At: 105, Action: "regeneration", Pct: 50}, // 01:45 (repeated)
		{At: 1320, Action: "off"},                  // 22:00
	}))

	var got []fireLog
	// 2026-04-05 transition at 02:00 LHDT = 2026-04-04 15:00 UTC.
	for u := atUTC(2026, 4, 4, 14, 0); !u.After(atUTC(2026, 4, 5, 12, 0)); u = u.Add(time.Minute) {
		tickAndLog(s, fc, u.In(lh), &got)
	}
	// 01:00 is the priming tick's minute (no catch-up); 01:45 fires once
	// at its first occurrence; the repeated 01:00 and 01:45 do not fire.
	is.Equal(got, []fireLog{
		{"04-05 01:45 +11", 105},
		{"04-05 22:00 +1030", 1320},
	})
}

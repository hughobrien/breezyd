# Schedule system — design

**Date:** 2026-05-06
**Status:** approved, pending implementation plan
**Issue:** [#25 — basic schedule system / cron](https://github.com/hughobrien/breezyd/issues/25)

## Goal

Add a daemon-driven 24-hour cyclic schedule per device. The user configures a small table of "at this time, set the unit to this state" entries via a new collapsible SCHEDULE block in the dashboard; `breezyd` writes the actions on schedule. There is no concept of days of the week or calendar dates — the schedule loops every 24 hours.

The schedule does not depend on the device's cloud service; all timing and writes are owned by `breezyd`.

## Non-goals

- No day-of-week or calendar-date scheduling.
- No CLI verbs (`breezy <name> schedule …`). Web UI is the only edit surface.
- No HomeKit exposure — schedule applies to the device but isn't visible from HomeKit.
- No catch-up on daemon startup or schedule re-enable: the schedule is event-driven, not state-driven.
- No "baseline" or "default" mode the schedule reverts to between entries. The unit holds whatever state the most recent entry (or user/sensor action) left it in.
- No per-entry sensor-override interaction. Sensor-driven boost (humidity / CO2 / VOC) and the night/turbo timer keep working exactly as today; the schedule just sets the underlying baseline at transition times.
- No metrics for schedule activity in `/metrics` (out of scope for v1; the `/v1/devices/{name}` JSON exposes everything the UI needs).

## Requirements (from issue + brainstorming)

1. **Per-device.** Each Breezy has its own SCHEDULE block, stored at `<state_dir>/schedule_<device>.json`.
2. **Single time per entry.** Each row is `At | Action | Pct`. (The original issue listed `From | To`; brainstorming simplified to a single `At` because the daemon only acts on transitions.)
3. **Action set:** `off`, `regeneration`, `ventilation`, `supply`, `extract`. The first powers the unit off; the rest power-on + set the airflow mode + set speed=manual at `pct`.
4. **Pct field** integer 10–100, always present. Greyed and `readonly` in the UI when Action=off; ignored at the wire in that case.
5. **Local time.** At-times are wall-clock in the daemon host's local timezone.
6. **No catch-up.** On daemon startup, schedule edit, or schedule re-enable, the daemon does NOT immediately apply the entry-in-effect. Only future transitions fire.
7. **Manual override is permitted between transitions.** User UI/HomeKit/CLI changes are not re-asserted by the schedule until the next entry's At-time arrives.
8. **Disable = no auto commands.** Toggling the enable checkbox off doesn't touch the unit; it only stops future entries firing.
9. **Retry on transient write failure.** Every 30s, capped at 10 minutes, abandoned early if the next entry's At-time arrives. ErrAuth is treated as a config error: log once, no retry.
10. **Alert surfaces in UI.** When the latest fire failed (with or without retries exhausted), the SCHEDULE panel auto-expands and shows a `⚠` warning line, mirroring how SENSORS auto-expands on humidity/CO2/VOC alerts.

## Architecture

```
                                            cmd/breezyd
                                    ┌───────────────────────────┐
   webui ── GET/PUT /schedule ────►│  Handler.{get,put}Schedule │
                                    └─────────┬─────────────────┘
                                              │ load/save
                                              ▼
                                    ┌──────────────────────┐
                                    │  Scheduler (per dev) │
                                    │  - in-mem schedule   │
                                    │  - state file        │
                                    │  - 1m ticker         │
                                    │  - retry state       │
                                    └──────┬───────────────┘
                                           │ at At-time, build action,
                                           │ acquire poller.LockUDP(),
                                           │ write via dialRecording
                                           ▼
                                  pkg/breezy/ops {SetMode, SetSpeedManual, Power}
                                           │
                                           ▼
                                   pkg/breezy.Client → device
```

- One `Scheduler` per device, started by `startPollers` next to the `EnergyTracker`. Cancelled by the same root `ctx`.
- State file at `<state_dir>/schedule_<device>.json` (atomic temp+rename, mode 0600). Loaded on construction; missing/malformed → empty disabled state + slog warn (matches `EnergyTracker.Load()`).
- Wire writes go through the existing `pkg/breezy/ops` helpers (`Power`, `SetMode`, `SetSpeedManual`). No new ops needed.
- Per-device UDP serialisation is provided by `Poller.LockUDP()`, the same mutex the poller and HTTP handlers acquire. The Scheduler is just another participant in the existing per-device serialisation discipline.
- Status-JSON integration mirrors `service.energy`: the handler reads from `Scheduler.Snapshot()` and glues `service.schedule` onto the existing `BuildStatus` output.

## Data model

### On-disk file (`<state_dir>/schedule_<device>.json`)

```json
{
  "version": 1,
  "enabled": true,
  "entries": [
    { "at": "08:00", "action": "regeneration", "pct": 60 },
    { "at": "22:00", "action": "off",          "pct": 60 }
  ],
  "last_apply": {
    "at":      "22:00",
    "fired":   "2026-05-06T22:00:14+01:00",
    "ok":      false,
    "err":     "device_unreachable: i/o timeout",
    "retries": 5
  }
}
```

- `entries` is sorted by `at` (HH:MM, 24hr). Stored sorted; loader sorts on read so a hand-edited file still works.
- `action` ∈ `{"off", "regeneration", "ventilation", "supply", "extract"}`. Unknown values reject the file.
- `pct` always present, integer 10–100. When `action == "off"` the field is stored but not written to the device.
- `last_apply` is daemon-managed — written after every fire attempt. Cleared by the handler on a successful PUT (a fresh schedule starts fresh).

### In-memory Go shape (`cmd/breezyd/scheduler.go`)

```go
type Scheduler struct {
    Device      string
    StateDir    string
    LockUDP     func() func()        // from poller; nil-safe
    NewClient   func() (HandlerClient, error)
    Recorder    func(writes []breezy.ParamWrite) // for cache writethrough + NoticeWrite
    Now         func() time.Time     // test seam; defaults to time.Now

    mu          sync.Mutex
    enabled     bool
    entries     []ScheduleEntry      // sorted by At
    lastApply   *LastApply
    retry       *retryState
    lastTick    ScheduleTime         // minute-of-day of the last tick (in-memory only)
    haveLastTick bool                // false until the first tick after Run() starts
}

type ScheduleEntry struct {
    At     ScheduleTime              // 0..1439, minute-of-day
    Action string
    Pct    int
}

type LastApply struct {
    At      ScheduleTime
    Fired   time.Time
    OK      bool
    Err     string
    Retries int
}

type retryState struct {
    entry       ScheduleEntry
    entryIndex  int                  // for supersede check
    attempts    int
    nextAttempt time.Time
    deadline    time.Time            // first attempt + 10m
}
```

### Action → wire writes

Resolved at fire time, all issued through one `recordingClient` so cache-update and `NoticeWrite` happen automatically:

| `action`        | writes issued                                                         |
|-----------------|------------------------------------------------------------------------|
| `off`           | `Power(false)`                                                         |
| `regeneration`  | `Power(true)` → `SetMode("regeneration")` → `SetSpeedManual(pct)`      |
| `ventilation`   | `Power(true)` → `SetMode("ventilation")`  → `SetSpeedManual(pct)`      |
| `supply`        | `Power(true)` → `SetMode("supply")`       → `SetSpeedManual(pct)`      |
| `extract`       | `Power(true)` → `SetMode("extract")`      → `SetSpeedManual(pct)`      |

`Power` first (cheap no-op when already on; needed when previous state was off). `SetSpeedManual` flips `0x02` to `0xFF` and writes `0x44`, putting the unit firmly into manual % regardless of which preset was active.

## HTTP API

Two new routes under `/v1/devices/{name}`:

### `GET /v1/devices/{name}/schedule`

Returns the in-memory state as the on-disk JSON shape (omitting `version`):

```json
{
  "enabled": true,
  "entries": [
    { "at": "08:00", "action": "regeneration", "pct": 60 }
  ],
  "last_apply": {
    "at": "08:00", "fired": "2026-05-06T08:00:00+01:00", "ok": true, "retries": 0
  }
}
```

200 always. `last_apply` omitted before the first fire. Reads from the Scheduler — no UDP.

### `PUT /v1/devices/{name}/schedule`

Replaces the schedule wholesale. Request body mirrors the on-disk JSON minus `last_apply`. On success: 200, body is the saved schedule (echo). On invalid input: 400 with `{"error": "...", "code": "bad_request"}`.

Validation rules:

- `entries[*].at` matches `^\d{2}:\d{2}$`, hours 0–23, minutes 0–59.
- `entries[*].action` ∈ the five-string set.
- `entries[*].pct` is integer 10–100. (For `action=off`, the field is required but its value is ignored.)
- No two entries share the same `at`.
- `entries` length ≤ 24.

Persistence: on a valid PUT, the handler swaps the in-memory schedule under `Scheduler.mu` AND atomically writes the file (temp + rename, mode 0600). `last_apply` is cleared. The running ticker picks up the new schedule on its next minute tick.

**Why PUT-not-PATCH:** the table is small (≤24 rows), the user always sees and edits the whole thing in the UI, and a wholesale-replace endpoint sidesteps "what's the entry index" / concurrent-edit concerns. Last write wins.

## Scheduler runtime

### Goroutine layout

```go
func (s *Scheduler) Run(ctx context.Context) {
    s.alignToNextMinute(ctx)              // sleep until next :00 second
    t := time.NewTicker(1 * time.Minute)
    defer t.Stop()
    for {
        s.tick(ctx, s.now())              // runs immediately after alignment
        select {
        case <-ctx.Done(): return
        case <-t.C:
        }
    }
}
```

`alignToNextMinute` sleeps `60s − (Now().Second() + ns/1e9)` so subsequent ticks land within a second of `:00`. `s.now()` honours `s.Now` for tests.

### `tick(now)` logic

1. Snapshot `enabled`, `entries`, `retry`, `lastTick` under `s.mu`.
2. If `!enabled`: clear any in-flight retry, return.
3. **Retry path.** If a retry is in flight:
   - If a future entry exists with At ≤ now (since the last tick): clear the retry; fall through to transition detection (rule D: superseded by next entry).
   - Else if `now ≥ retry.nextAttempt` and `now < retry.deadline`: call `fire(retry.entry)`.
   - Else if `now ≥ retry.deadline`: clear retry; leave `lastApply.ok=false` so the UI keeps the alert.
   - Else: nothing to do this minute.
4. **Transition detection.** Find any entry whose At lies in the half-open window `(lastTick, nowMinute]`.
   - On daemon startup, `haveLastTick == false`: the first tick records `lastTick = nowMinute`, sets `haveLastTick = true`, and fires nothing (Q5 contract: no catch-up). The sentinel exists because minute-of-day 0 (00:00) is itself a valid value, so a plain `lastTick == 0` zero-check would mis-fire the first time the daemon happened to start at midnight.
   - **Midnight wraparound.** If `nowMinute < lastTick` (the tick crossed 24:00 → 00:00), evaluate the window as the union `(lastTick, 1440) ∪ [0, nowMinute]`. In practice the wall-clock skip from 23:59 → 00:00 is one minute, so this almost always lights up at most one entry; the union form just keeps the logic correct under longer pauses.
   - If multiple entries match the window (daemon paused for >1m crossed several At-times), fire only the **latest** one in that window. Earlier ones are stale.
5. Update `lastTick = nowMinute` and persist if any state changed.

### `fire(entry)` logic

Synchronous within the tick goroutine:

1. Acquire `LockUDP()` — same mutex the poller and HTTP handlers use.
2. Build the writes per the action table.
3. Open a client via `NewClient()`; defer `Close`.
4. Wrap in `recordingClient` so cache update + `NoticeWrite` happen on success.
5. Use `context.WithTimeout(ctx, 5*time.Second)` (matches existing handler timeouts).
6. Call `breezy.Power`, `breezy.SetMode`, `breezy.SetSpeedManual` sequentially.
7. **On success:** clear retry state, set `lastApply{ok=true, retries=existing or 0, ...}`, persist.
8. **On `ErrAuth`:** log once at WARN, set `lastApply{ok=false, err="auth_failed: ..."}`, do NOT install retry. Persist.
9. **On other errors:** install retry state (`entry`, `attempts=1`, `nextAttempt = now + 30s`, `deadline = now + 10m`). Set `lastApply{ok=false, retries=1}`. Persist.
10. Release `LockUDP`.

### Retry state machine

- `nil → active`: any non-auth fire failure.
- `active → nil` on any of: fire succeeds, `now ≥ deadline`, a newer entry's At-time arrives in the same tick (supersede), user edits the schedule (handler clears it), `enabled=false`.
- `active → active+1`: minute tick where `now ≥ nextAttempt`; bump `attempts`, set `nextAttempt = now + 30s`.

### Persistence on every transition

`last_apply` and the schedule live in the same file. On any state change (fire success, fire failure, retry attempt, retry abandoned, user edit), atomic temp+rename. The file is small (<1KB) so the I/O cost is negligible.

### Test seams

- All time reads via `s.Now()`.
- `tick(ctx, now)` is package-private and called directly in tests with synthetic times.
- The UDP write path uses an injected `NewClient` returning a fake `HandlerClient` (same pattern as `Poller.NewClient`).
- The 1m ticker is real but tests never run `Run` — they drive `tick` synchronously instead. This avoids time-dependent flakes.

## Status-JSON integration

`getDevice` glues `service.schedule` onto the `BuildStatus` output, mirroring how `service.energy` is included today:

```jsonc
// GET /v1/devices/livingroom (excerpt)
{
  "service": {
    "energy":   { "today": { ... }, "lifetime": { ... } },
    "schedule": {
      "enabled": true,
      "entries": [
        { "at": "08:00", "action": "regeneration", "pct": 60 },
        { "at": "22:00", "action": "off",          "pct": 60 }
      ],
      "alert":   true,
      "last_apply": {
        "at": "22:00", "fired": "2026-05-06T22:00:14+01:00",
        "ok": false, "err": "device_unreachable: i/o timeout", "retries": 5
      }
    }
  }
}
```

`alert` is a derived bool (`last_apply != nil && !last_apply.ok`) so the UI doesn't have to climb into `last_apply` to decide whether to force-expand. Same shape as the `alerts.{humidity,co2,voc}` flags.

The handler reads from `Scheduler.Snapshot()` (in-memory) — no extra UDP, same cost as the energy block.

## UI block

New `<details class="block schedule">` in `cmd/breezyd/ui/index.html`, placed immediately above the existing `<div class="controls">` block. Collapsed by default; force-expanded when `service.schedule.alert === true` (same JS pattern as the SENSORS block).

```
▶ SCHEDULE                          [ ☐ enabled ]
─────────────────────────────────────────────────
[+ add entry]                              [save]
┌──────┬───────────────┬────┬───┐
│ At   │ Action        │Pct │ × │
├──────┼───────────────┼────┼───┤
│08:00 │ [regenerat. ▼]│ 60 │ × │
│22:00 │ [off        ▼]│  — │ × │   ← pct greyed when action=off
└──────┴───────────────┴────┴───┘
⚠ last apply 22:00 failed: device_unreachable     ← only when alert
   retried 5 times
```

- `<input type="time">` for At (browser-native HH:MM picker; no JS).
- `<select>` for Action with the five options.
- `<input type="number" min="10" max="100">` for Pct. `readonly` + greyed when Action=off; user-typed values <10 are clamped to 10 on save (matches the firmware floor).
- `[+ add entry]` appends a new row with defaults (`08:00`, `regeneration`, `60`).
- `×` removes a row.
- The enable checkbox and the table are persisted together on `[save]` — one PUT, the whole document. No per-cell autosave.
- Validation surfaces inline: invalid rows highlighted red, `[save]` disabled with tooltip ("two entries at 10:00", "pct must be 10-100").
- The alert footer renders only when `service.schedule.alert === true`, using the existing `.warn` style class. Force-expand sets `<details open>` on render; the user can still close it manually.
- On `.stale` cards (>90s no successful poll), the SCHEDULE block follows the existing card-wide opacity/grayscale; `[save]` is disabled (consistent with the controls block).

The new collapsed-state lines are: the "▶ SCHEDULE" summary and (when alert is set) the warn footer. Steady-state card height is essentially unchanged.

## Tests

### Go unit tests (`cmd/breezyd/scheduler_test.go`, new file)

- `TestScheduler_FiresOnAtTime` — fake clock, advance to 08:00, assert fake client sees `Power(true)` + `SetMode("regeneration")` + `SetSpeedManual(60)` writes in order.
- `TestScheduler_OffActionPowersDown` — assert action=off writes only `Power(false)`.
- `TestScheduler_NoCatchupOnStartup` — Now=14:00, schedule has 08:00 entry; first tick fires nothing.
- `TestScheduler_FiresAcrossMidnight` — schedule has 00:05 entry; advance lastTick from 23:59 to 00:06, assert the 00:05 entry fires once via the wraparound union window.
- `TestScheduler_DisabledIsInert` — enabled=false, advance through every entry's At-time, no writes.
- `TestScheduler_EditClearsLastApply` — pre-seed `lastApply` failure, simulate PUT, assert `lastApply` is cleared and retry abandoned.
- `TestScheduler_DuplicateAtRejected` — `validate()` returns `ErrInvalidArg` on two 10:00 entries.
- `TestScheduler_PersistsAcrossReload` — write a schedule, drop the Scheduler, build a new one with same StateDir, assert in-memory state matches.
- `TestScheduler_MalformedFileStartsEmpty` — write garbage JSON, construct Scheduler, assert empty disabled state + slog warn.
- Retry state machine cluster:
  - `TestRetry_TimeoutInstallsRetry` — fake client returns timeout; retry installed with attempts=1, nextAttempt=now+30s.
  - `TestRetry_AuthFailsNoRetry` — fake client returns ErrAuth; retry NOT installed; `lastApply.err` mentions `auth_failed`; ticking forward fires nothing.
  - `TestRetry_SucceedsClearsRetry` — first fire fails, advance 30s, second fire succeeds, retry cleared, `lastApply.ok=true`.
  - `TestRetry_DeadlineAbandons` — all attempts fail, advance past 10min deadline, retry cleared, `lastApply.ok` stays false.
  - `TestRetry_SupersededByNextEntry` — retry in flight for 10:00 entry, advance past 11:00 entry's At-time, assert 10:00 retry dropped and 11:00 fires.

### HTTP handler tests (`cmd/breezyd/server_test.go`, extend)

- `TestHandler_GetSchedule_Empty` — fresh device, returns `{enabled:false, entries:[]}`.
- `TestHandler_PutSchedule_Roundtrip` — PUT a valid schedule, assert 200 + GET returns same.
- `TestHandler_PutSchedule_Validation` — table-driven over each rule (bad At, bad action, pct=5, duplicate At, >24 entries) → 400 with `code:"bad_request"`.
- `TestHandler_PutSchedule_PersistsToDisk` — PUT, then read the file directly, assert JSON shape on disk.
- The existing `TestHandler_NotFound_OnUnknownDevice` covers GET/PUT 404 once routes are registered.

### Playwright (`tests/ui/`, new spec file)

- **Empty state** — `service.schedule = {enabled:false, entries:[]}`; assert SCHEDULE block exists, collapsed, summary visible, no warn footer.
- **Populated state** — schedule with two entries; expand the panel, assert table renders both rows with correct cells.
- **Action=off greys pct** — change a row's action to off; assert pct input has `readonly` and is greyed.
- **Validation feedback** — set two rows to the same At; assert `[save]` is disabled and conflicting rows have an error class.
- **Alert force-expand** — `service.schedule.alert = true`; assert panel is rendered open and the warn line is visible.
- **Save round-trip** — mock PUT to capture the request body; click `[save]`; assert the captured body matches the table state.

### Screenshot regen

After UI lands, run `just screenshot` and commit the updated PNGs. The README's 3-col screenshot will pick up the new collapsed-summary line on every card.

### Live integration test

Out of scope. Driving a schedule fire against real hardware would write on a timer with no clean way to cleanup-restore prior state, which violates the "no unsanctioned writes" rule. The protocol-side writes (`Power`, `SetMode`, `SetSpeedManual`) are already covered by the existing `pkg/breezy/integration_test.go`.

## Documentation

- `README.md` — add a SCHEDULE entry to the dashboard feature list.
- `CHANGELOG.md` — entry for the next release.
- `CLAUDE.md` — short subsection under "Architecture" documenting the per-device `Scheduler`, its state-file convention, the no-catch-up contract, and the retry policy. Same level of detail as the existing "Energy tracking" subsection.

## Open questions

None as of design approval. Implementation plan to follow.

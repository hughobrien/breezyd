# Poller: preserve `Snapshot.LastPoll` across failed ticks

**Date:** 2026-05-09
**Issue:** [#178](https://github.com/hughobrien/breezyd/issues/178)
**Surfaced by:** PR #177 (the un-skip attempt on the Playwright stale-class test).

## Problem

`cmd/breezyd/poller.go` updates the cached `Snapshot.LastPoll` timestamp on
every tick, including failed ones:

- Line ~181: dial-failure path writes `LastPoll: p.now()` with `LastErr: err`.
- Line ~221: read-failure path writes `LastPoll: p.now()` with `LastErr: lastErr`.

The dashboard computes staleness as `time.Since(snap.LastPoll) > staleWindow`
(`cmd/breezyd/ui_view.go::snapshotToView`). With the current poller behavior,
age maxes out at one poll interval — every failed tick resets it. The
3×poll-interval stale threshold described in `SPECIFICATION-web.md` "Card
states" can therefore never fire under sustained UDP timeouts.

The same bug affects `breezyd_last_poll_timestamp` (`cmd/breezyd/metrics.go`).
`SPECIFICATION-daemon.md` line 368 says staleness is "signalled exclusively by
`breezy_last_poll_timestamp` and `breezy_up`, which Prometheus operators are
accustomed to alerting on." The standard Prometheus alert pattern
(`time() - breezy_last_poll_timestamp > X`) cannot fire today either —
the timestamp ticks forward on every failed poll.

`tests/ui/dashboard.spec.ts:556` ("stale class applied via signal patch
preserves card identity") is `test.skip`'d with a multiline comment pointing at
this issue.

## Decision: Approach A (preserve `LastPoll` on failed ticks)

`Snapshot.LastPoll` is redefined as **the wall-clock time of the most recent
*successful* poll**. Failed ticks no longer overwrite it.

### Why A over B

The issue offers two options:

- **A.** Stop updating `LastPoll` on failed ticks. Semantic shift only;
  no schema change.
- **B.** Add a separate `LastSuccessfulPoll` field; keep `LastPoll` as
  "last attempt." Additive but doubles the timestamps in the JSON snapshot
  and forces every `LastPoll` consumer (dashboard, metric) to switch to the
  new field anyway.

A is chosen because:

1. The metric `breezyd_last_poll_timestamp` is documented to signal
   staleness. That documentation only holds true if the timestamp tracks
   successful polls — A *fixes* the metric; B requires a parallel rename
   to `breezyd_last_successful_poll_timestamp` or a silent semantic swap on
   the existing metric (same shift A makes, just delayed).
2. No external consumer in this repo reads `last_poll` from `/v1/devices`
   for "last attempt" semantics. The CLI's `breezy <name> status` consumes
   `pkg/breezy.Status.LastPoll`, a *standalone-mode* construct populated
   directly from the UDP read in `pkg/breezy/ops`, not from the daemon
   snapshot — unaffected by this change.
3. Adding a new JSON field for a hypothetical caller is YAGNI.

## Scope

### In scope

- `cmd/breezyd/poller.go` — drop `LastPoll: p.now()` from both failure paths;
  carry the prior `LastPoll` (and `Values`, mirroring the existing failed-poll
  cache semantics) forward instead.
- `cmd/breezyd/state.go` — update the `Snapshot.LastPoll` doc comment to
  reflect the new "most recent successful poll" semantics.
- `SPECIFICATION-daemon.md` — extend the "failed-poll cache semantics"
  paragraph (line ~195) with one sentence noting that `LastPoll` is also
  preserved across failed ticks.
- New Go test: `cmd/breezyd/poller_test.go` — covers both the dial-failure and
  read-failure paths.
- `tests/ui/dashboard.spec.ts:556` — un-skip the stale-class test, replace
  the multiline `test.skip` justification comment with a one-liner.

### Out of scope

- No new `Snapshot.LastSuccessfulPoll` field.
- No JSON field rename in `/v1/devices` or `/v1/devices/{name}` (`last_poll`
  stays; only its meaning shifts).
- No metric rename; `breezyd_last_poll_timestamp` is correctly named for the
  new semantics.
- `pkg/breezy.Status.LastPoll` — separate construct used by standalone CLI
  mode. Untouched.

## Implementation sketch

In `cmd/breezyd/poller.go::tick`:

1. Read the prior snapshot once at the top of the function:
   `prev, _ := p.State.Get(p.Name)`. (`Get` returns a deep copy on a
   not-found key; `prev.LastPoll` will be the zero `time.Time` and
   `prev.Values` will be nil — both safe carry-forward defaults.)
2. **Dial-failure branch:** emit
   `Snapshot{IP: p.IP, Values: prev.Values, LastPoll: prev.LastPoll, LastErr: err}`.
   Today this branch overwrites `Values` with empty *and* `LastPoll` with
   `p.now()`; both behaviors change to "carry forward."
3. **Read-failure branch:** the existing branch already preserves `Values`
   when `lastErr != nil && len(values) == 0`. Extend it to also use
   `prev.LastPoll` instead of `p.now()` for the failure case. Refactor to:

   ```go
   newLastPoll := p.now()
   if lastErr != nil {
       newLastPoll = prev.LastPoll
   }
   ```

   …then `LastPoll: newLastPoll` in the `Snapshot` literal.

The success path is unchanged: `LastPoll: p.now()`.

### Edge case: very first tick fails

If the daemon starts and the first poll fails, `prev.LastPoll` is zero. The
new snapshot's `LastPoll` is therefore zero. `snapshotToView` already handles
zero correctly: "no poll yet → `Stale = true`." The Prometheus exporter at
`metrics.go:321-326` skips `lastPollTimestamp` when `LastPoll.IsZero()`, so
the gauge simply doesn't emit until the first successful poll — the right
thing for an alert pattern.

## Tests

### Go unit (new)

A test in `cmd/breezyd/poller_test.go` modelled on the existing
`TestPoller_FanSettle_*` shape:

1. Build a `Poller` against an in-process `MemClient` seeded from
   `pkg/breezy/fakedevice/snapshot_148.json`.
2. Run one `tick(ctx)`. Assert the resulting snapshot has
   `LastPoll == T1` (some non-zero time), `LastErr == nil`, `len(Values) > 0`.
3. Flip `MemClient.SetTimeoutMode(true)`. Advance the poller's `now` clock
   so the next tick would normally overwrite `LastPoll`. Run a second tick.
   Assert: `LastPoll == T1` (preserved), `LastErr != nil`,
   `len(Values) == len(values_from_T1)` (carried forward).
4. Flip `MemClient.SetTimeoutMode(false)`. Run a third tick. Assert
   `LastPoll > T1` (the success path advances) and `LastErr == nil`.

Coverage points:
- read-failure path preserves `LastPoll`,
- dial-failure path preserves `LastPoll` (a separate sub-test that sets the
  Poller's `dial` to a closure returning `error` to exercise the dial branch),
- success-path `LastPoll` resumes advancing once the failure clears.

### Playwright (un-skip)

`tests/ui/dashboard.spec.ts:556` (`stale class applied via signal patch
preserves card identity`):

- Remove the `test.skip` → `test`.
- Strip the multiline justification comment that points at this issue;
  replace with a one-line reference to "SPECIFICATION-web.md 'Card states'."
- Existing `{ timeout: 8_000 }` already exceeds the 3×poll_interval (3s with
  the test daemon's `poll_interval = "1s"`) plus generous slack. No change
  required to the timeout itself.

### Verification gates

- `just check` — fast pre-commit (lint + fast tests + templ-drift).
- `just test-ui` — Playwright now runs the previously-skipped stale test.
- `just check-all` — full pre-push gate (race + Playwright).

## Risks / known limitations

- **Behavior change for any operator scraping `/v1/devices` for "last attempt"
  semantics.** None known in this repo. The metric was already documented to
  reflect successful polls, so external dashboards/alerts following the spec
  will start working correctly rather than break.
- **Per-call state reads.** `tick()` adds one `p.State.Get(p.Name)` per tick.
  This is a `sync.RWMutex.RLock` + map lookup + struct copy — negligible at
  one tick per poll interval per device.

## Acceptance checklist

From issue #178 plus the implementation:

- [x] Approach decided (A).
- [ ] Poller change in `cmd/breezyd/poller.go` (both failure paths).
- [ ] `Snapshot.LastPoll` doc comment updated in `cmd/breezyd/state.go`.
- [ ] `SPECIFICATION-daemon.md` failed-poll cache paragraph extended.
- [ ] Go test in `cmd/breezyd/poller_test.go` covering both failure paths and
      the success-path resumption.
- [ ] Playwright stale-class test un-skipped in `tests/ui/dashboard.spec.ts`.
- [ ] `just check-all` green.

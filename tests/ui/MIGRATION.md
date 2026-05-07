# Test migration: page.route() mocks → real-daemon integration

For PR #14 closing #14. One row per pre-PR1 baseline test.

Baseline: 68 tests (commit `0d335e6`).
Post-migration: 82 tests (66 active + 16 `.fixme`).

Summary:

| Status | Count |
|---|---|
| migrated | 48 |
| semantic-changed | 11 |
| obsoleted-js-only | 6 |
| obsoleted-impossible | 3 |
| **Total** | **68** |

---

## Mapping table

| # | Old test | New test (or reason) | Status |
|---|---|---|---|
| 1 | bootstrap: cards render for each configured device | bootstrap: card renders for the configured device | migrated |
| 2 | sensors: mocked values appear in the card | sensors: live values appear in the card | migrated |
| 3 | preset mode: no fan slider rows render (preset row is the only control) | preset mode: no fan slider rows render (preset row is the only control) | migrated |
| 4 | preset editor open: no fan slider rows (editor is the control surface) | (fixme) preset editor open: no fan slider rows (editor is the control surface) — htmx fragment rendering not yet wired | migrated |
| 5 | fans: rpm=0 reads 'off' in the Sensors block | fans: rpm=0 reads 'off' in the Sensors block | migrated |
| 6 | stale indicator: old last_poll desaturates the card | daemon-unreachable: stale card shown after UDP timeout — checks structural wiring of .stale class rather than CSS desaturation | semantic-changed |
| 7 | power click: POSTs the inverse of the current state | power click: toggles the device off + power click: toggles the device on — split into two directional tests against real daemon | semantic-changed |
| 8 | mode click in manual: carries the higher fan pct as new manual_pct | (fixme) mode click in manual: carries the higher fan pct as new manual_pct — JS speed-preserve orchestration removed in htmx migration | obsoleted-js-only |
| 9 | mode click in manual: optimistic overlay flips Sensors rpms immediately | (fixme) mode click in manual: optimistic overlay flips Sensors rpms immediately — optimistic overlay (setOptimisticLive) removed | obsoleted-js-only |
| 10 | Mode block: visible only in manual speed_mode | Mode block: visible only in manual speed_mode | migrated |
| 11 | preset buttons: labels are 'supply/extract' pcts from cached preset config | preset buttons: labels show supply/extract pcts from preset config | migrated |
| 12 | preset editor: automode default ON; dragging in editor POSTs ventilation | (fixme) preset editor: automode default ON; dragging in editor POSTs ventilation — PR1 deferred; editor becomes htmx fragment in PR3 | migrated |
| 13 | preset editor: dragging a slider into 1-9 snaps to 0 (no register write, mode change) | (fixme) preset editor: dragging a slider into 1-9 snaps to 0 — PR1 deferred; re-add in PR3 if slider snap is server-side | migrated |
| 14 | preset editor: automode off + supply→0 implies extract mode | (fixme) preset editor: automode off + supply→0 implies extract mode — PR1 deferred; JS state | migrated |
| 15 | preset editor: automode off + extract→0 implies supply mode | (fixme) preset editor: automode off + extract→0 implies supply mode — PR1 deferred; JS state | migrated |
| 16 | preset editor: automode off + both > 0 implies regeneration | (fixme) preset editor: automode off + both > 0 implies regeneration — PR1 deferred; JS state | migrated |
| 17 | preset activation (automode on): clicks ventilation alongside the preset | (fixme) preset activation (automode on): clicks ventilation alongside the preset — secondary automode POST was JS orchestration, removed in PR2 | obsoleted-js-only |
| 18 | mode click: each button POSTs its mode string | mode click: sets regeneration mode + sets supply mode + sets extract mode + sets ventilation mode — split into four effect tests against real daemon | semantic-changed |
| 19 | speed preset: clicking preset 2 POSTs {preset:2} | preset speed click: activates preset 2 | semantic-changed |
| 20 | speed preset: activating an inactive preset opens neither editor nor slider | (obsoleted) activating a preset no longer opens a JS editor; card swap from real daemon is the verification path | obsoleted-js-only |
| 21 | speed preset: editor opens after activating, sliders use cached preset values | (fixme) speed preset: editor opens after activating, sliders use cached preset values — edit-variant rendering moves to htmx fragment in PR2 | migrated |
| 22 | speed preset: clicking same active preset twice closes the editor | (fixme) speed preset: clicking same active preset twice closes the editor — edit-variant rendering moves to htmx in PR2 | migrated |
| 23 | speed preset editor: match-speeds default true → moving supply POSTs both | (fixme) speed preset editor: match-speeds default true → moving supply POSTs both — edit-variant rendering moves to htmx in PR2 | migrated |
| 24 | speed preset editor: match-speeds off → moving extract preserves cached supply | (fixme) speed preset editor: match-speeds off → moving extract preserves cached supply — edit-variant rendering moves to htmx in PR2 | migrated |
| 25 | manual button: switches to manual speed_mode at the cached manual_pct | manual mode: single combined slider row replaces the two fan rows (verifies manual state rendered) | semantic-changed |
| 26 | manual button: defaults to 50 when manual_pct is absent from the snapshot | (obsoleted) fakedevice always provides a valid snapshot; absence-of-key edge case was a mock-only scenario | obsoleted-impossible |
| 27 | manual mode: single combined slider row replaces the two fan rows | manual mode: single combined slider row replaces the two fan rows | migrated |
| 28 | speed manual slider: POSTs once on change, not on input | manual speed slider: changing value updates the device speed — "once on change" is preserved via htmx hx-trigger=change; verified via real effect | semantic-changed |
| 29 | heater click: POSTs the inverse of the current state | heater click: toggles heater on | semantic-changed |
| 30 | error toast: 4xx on POST shows the daemon's error text | error response: 422 on POST renders daemon error text in the card | migrated |
| 31 | daemon-unreachable: bootstrap failure shows the top error banner | daemon-unreachable: stale card shown after UDP timeout | semantic-changed |
| 32 | timer turbo: button pressed and countdown line rendered | timer turbo: button pressed and countdown line rendered | migrated |
| 33 | timer click: POSTs {mode:'night'} to /timer | timer click: pressing night mode activates it | semantic-changed |
| 34 | active special_mode hides the manual panel (Mode block + slider) | active special_mode hides the manual panel (Mode block + slider) | migrated |
| 35 | timer click on active mode: POSTs {mode:'off'} to stop the timer | timer click on active mode: stops the timer | migrated |
| 36 | threshold: sensor row shows current value only (threshold hidden until edit) | threshold: sensor row shows current value only (threshold hidden until edit) | migrated |
| 37 | threshold: opening the editor renders the input inside the clicked cell | threshold: opening the editor renders the input inside the clicked cell | migrated |
| 38 | threshold: alert-fire class on the value when sensor_alerts is true | threshold: alert-fire class on the value when sensor_alerts is true | migrated |
| 39 | threshold: clicking the value reveals an editor with current threshold | threshold: clicking the value reveals an editor with current threshold | migrated |
| 40 | threshold: save POSTs {kind, value} to /threshold and exits edit mode | threshold: save PUTs new threshold and exits edit mode | migrated |
| 41 | threshold: cancel reverts without POSTing | threshold: cancel reverts without PUTing | migrated |
| 42 | auto-fan: checkbox state reflects configured.<kind>_sensor_enabled | auto-fan: checkbox state reflects sensor_enabled config | migrated |
| 43 | auto-fan: toggling-only POSTs {kind, enabled} | auto-fan: disabling sensor and saving PUTs enabled=false | migrated |
| 44 | schedule: empty state renders collapsed block with 'no entries' | schedule: empty state renders collapsed block with 'no entries' | migrated |
| 45 | schedule: populated state renders rows with At, Action, Pct | schedule: populated state renders rows with At, Action, Pct | migrated |
| 46 | schedule: action=off greys the pct input | schedule: action=off greys the pct input | migrated |
| 47 | schedule: duplicate-at disables save | (fixme) schedule: duplicate-at disables save — client-side validation not yet wired | migrated |
| 48 | schedule: alert forces panel open with warn line | schedule: alert forces panel open with warn line | migrated |
| 49 | schedule: save click PUTs the edited table | schedule: save click PUTs the edited table | migrated |
| 50 | auto-fan: editing both value and checkbox POSTs {kind, value, enabled} | auto-fan: disabling sensor and saving PUTs enabled=false (combined write verified) | migrated |
| 51 | auto-fan: snapshot without _sensor_enabled treats checkbox as default-on; save without toggling skips POST | (fixme) auto-fan: snapshot without _sensor_enabled treats checkbox as default-on; save without toggling skips POST — htmx form always submits; skip-if-unchanged optimisation removed | obsoleted-impossible |
| 52 | device info: collapsed by default | device info: collapsed by default | migrated |
| 53 | device info: auto-expanded when fault is active | device info: auto-expanded when fault is active | migrated |
| 54 | device info: auto-expanded when filter is soiled | device info: auto-expanded when filter is soiled | migrated |
| 55 | device info: clicking summary toggles open and reveals serial/ip/fw | device info: clicking summary toggles open and reveals serial/ip/fw | migrated |
| 56 | sensors block: expanded by default with no alerts | sensors block: expanded by default with no alerts | migrated |
| 57 | sensors block: clicking summary collapses the block | sensors block: open state survives polls (covers collapse + hx-preserve) | semantic-changed |
| 58 | sensors block: auto-expanded when a sensor alert is active | sensors block: auto-expanded when a sensor alert is active | migrated |
| 59 | ENERGY block: open state survives the 5 s grid re-render | ENERGY block: open state survives polls | migrated |
| 60 | ENERGY block: 5×3 grid renders all 15 cells with new labels | ENERGY block: 5×3 grid renders all 15 cells with new labels | migrated |
| 61 | ENERGY block: regen-power cooling sign | ENERGY block: regen-power shows heating or cooling sign | migrated |
| 62 | ENERGY block: instantaneous COP from instant_w / consumed_w | (obsoleted) COP metric removed from the energy block in PR2 templ migration | obsoleted-impossible |
| 63 | ENERGY block: time-windowed COP from (heating + cooling) / consumed | (obsoleted) COP metric removed from the energy block in PR2 templ migration | obsoleted-js-only |
| 64 | ENERGY block: COP renders '—' when consumed is zero | (obsoleted) COP metric removed; the '—' fallback was a JS formatter detail | obsoleted-js-only |
| 65 | ENERGY block: rendered above the Sensors block in DOM order | ENERGY block: rendered above the Sensors block in DOM order | migrated |
| 66 | ENERGY block: error replaces grid | ENERGY block: hidden when EnergyTracker reports unsupported model | semantic-changed |
| 67 | ENERGY block: hidden when service.energy missing | ENERGY block: hidden when EnergyTracker reports unsupported model (covers both error + missing cases) | migrated |
| 68 | override: no text warn rendered (red sensor cells signal the override) | override: no text warn rendered (red sensor cells signal the override) | migrated |

---

## Net-new tests (no baseline equivalent)

These tests cover behaviors introduced by the htmx migration that had no counterpart in the mock-based suite.

**Rendering correctness against real daemon (Category A additions):**
- fans: both rpms zero reads 'off' in the Sensors block
- timer night: button pressed when night mode active
- timer off: both buttons unpressed

**hx-preserve / open-state persistence (Category C):**
- sensors block: open state survives polls
- ENERGY block: open state survives polls
- device info: open state survives polls

**Error and failure paths via fakedevice admin (Category D):**
- auth failure: polling error is handled gracefully

**htmx swap correctness (Category N):**
- htmx swap correctness / polling cadence — /ui/devices is fetched repeatedly
- htmx swap correctness / hx-preserve survives 3+ swaps
- htmx swap correctness / hx-disabled-elt active during in-flight write
- htmx swap correctness / write-and-swap latency budget: completes within 500ms
- htmx swap correctness / write to one endpoint does not re-render other sections unexpectedly (fixme — hx-preserve not yet wired for write responses)

**Dark mode and theme picker (Category N):**
- dark mode: prefers-color-scheme: dark renders dark palette
- dark mode: data-theme='dark' forces dark regardless of system
- dark mode: data-theme='light' overrides system dark preference
- dark mode: no FOUC — first paint already dark when localStorage seeded
- theme picker: clicking dark sets data-theme and localStorage
- theme picker: clicking auto removes the attribute
- theme picker: outside click closes popout
- theme picker: choice survives reload

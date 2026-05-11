# Schedule Inline-Edit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the schedule block's two-mode (read/edit) UX into a single always-editable view with per-field autosave and a small `+` button at the bottom.

**Architecture:** Single templ `ScheduleBlock` template with inline-editable inputs in every row. Each input's `data-on:change__debounce.200ms` PUTs the whole form to the unchanged `PUT /ui/devices/{name}/schedule` endpoint. The handler returns `200 OK` + empty SSE on success (so the focused input isn't clobbered) and `errorBannerSSE` on validation failure. The schedule details carry `data-edit="true"` whenever `[open]`, so the push pipeline's existing `[data-block="schedule"]:not([data-edit])` selector filters patches while the user is interacting. Driven by spec at `docs/superpowers/specs/2026-05-11-schedule-inline-edit-design.md`.

**Tech Stack:** Go 1.x, a-h/templ v0.3.x, datastar-go v1.2.x (SDK), datastar v1.0.1 (client), Playwright v1.59.

---

### Task 1: `putUISchedule` returns 200 + empty SSE on success

**Goal:** Stop sending the read fragment back from the autosave handler, so a successful field-edit's response doesn't overwrite the user's currently-focused input.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` (the `putUISchedule` function)
- Modify: `cmd/breezyd/handlers_ui_write_test.go` (`TestUISchedulePut_Happy`)

**Acceptance Criteria:**
- [ ] `putUISchedule` on a valid form returns `200 OK` + `Content-Type: text/event-stream` with no `event: datastar-patch-elements` lines.
- [ ] The scheduler's state still updates (entries persisted, notify still fires).
- [ ] Validation failures still emit a `datastar-patch-elements` event into `#global-error-banner` with the message and the `Datastar-Status` header set to 422 (unchanged from today's behavior).
- [ ] `TestUISchedulePut_Happy` asserts the body contains no fragment.
- [ ] `TestUISchedulePut_BadForm_*` tests continue to pass (validation paths unchanged).

**Verify:** `go test ./cmd/breezyd/ -run TestUISchedulePut -v` → all subtests PASS

**Steps:**

- [ ] **Step 1: Read the current `putUISchedule` to confirm the success path.**

Look at `cmd/breezyd/handlers_ui_write.go` for the `putUISchedule` function. The success path today ends with `h.scheduleReadFrag(w, r, name)`. We'll replace that with an empty SSE response.

- [ ] **Step 2: Add an empty-SSE helper.**

In `cmd/breezyd/handlers_ui_write.go`, just after the `scheduleEditFrag` helper, add:

```go
// scheduleAcknowledgeSSE writes a 200 OK SSE response with no
// datastar-patch-elements events — the autosave handler's success
// path. The form's DOM state is already correct on the client (the
// user just typed it); sending back a re-rendered fragment would
// clobber whatever input still has focus. The next regular poll
// cycle refreshes the block once the user collapses or moves on.
//
// Mirrors errorBannerSSE's shape but emits no events. The SSE
// content-type + 200 status is still required so datastar's @put
// fetch action processes the response cleanly rather than treating
// it as an error.
func scheduleAcknowledgeSSE(w http.ResponseWriter, r *http.Request) {
	sse := newSSE(w, r)
	_ = sse // keep the import dependency; emitting no events.
}
```

- [ ] **Step 3: Replace the success branch in `putUISchedule`.**

In `cmd/breezyd/handlers_ui_write.go`, find the success path of `putUISchedule`:

```go
	if err := sch.Replace(enabled, entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			h.scheduleEditFrag(w, r, name, err.Error())
			return
		}
		h.uiWriteError(w, r, err)
		return
	}
	h.scheduleReadFrag(w, r, name)
```

Change the final line:

```go
	if err := sch.Replace(enabled, entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			h.scheduleEditFrag(w, r, name, err.Error())
			return
		}
		h.uiWriteError(w, r, err)
		return
	}
	scheduleAcknowledgeSSE(w, r)
```

(The notify-after-write fan-out still happens via the scheduler subsystem on next poll; no separate `notifyAfterWrite` call needed here because `Replace` already persists and the poller picks up the new state.)

- [ ] **Step 4: Update `TestUISchedulePut_Happy`.**

In `cmd/breezyd/handlers_ui_write_test.go`, find `TestUISchedulePut_Happy`. Replace the body-content assertion so it asserts the response is an SSE stream with **no** `event: datastar-patch-elements` line:

```go
	is.Equal(resp.StatusCode, 200)
	is.Equal(resp.Header.Get("Content-Type"), "text/event-stream")
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(!strings.Contains(bs, "event: datastar-patch-elements")) // success path emits no patch
```

Drop any assertions on `bs` containing read-variant strings (`no entries`, `class="block schedule"`, etc.) — those belong to the next-poll refresh, not the autosave response.

- [ ] **Step 5: Run the targeted tests to verify.**

```sh
go test ./cmd/breezyd/ -run TestUISchedulePut -v
```

Expected: every `TestUISchedulePut_*` subtest passes. Validation paths (`TestUISchedulePut_BadForm_InvalidTime`, etc.) still 422 with the banner fragment.

- [ ] **Step 6: Commit.**

```sh
git add cmd/breezyd/handlers_ui_write.go cmd/breezyd/handlers_ui_write_test.go
git commit -m "feat(ui): putUISchedule success returns empty SSE, not the read fragment

So an autosave-success response doesn't clobber the user's
currently-focused input. The form's DOM state is already correct
client-side; the next poll cycle refreshes the block.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

### Task 2: Consolidate the schedule template + remove the edit endpoint

**Goal:** One templ template (`ScheduleBlock`) renders the always-editable view; the `/ui/devices/{name}/schedule/edit` GET endpoint disappears; row fields autosave via per-input `data-on:change`. The `<details>` carries `data-edit="true"` whenever it's `[open]` so the push pipeline's existing filter prevents repaints from clobbering mid-edit values.

**Files:**
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ`
- Auto-regenerated: `cmd/breezyd/ui/templates/schedule_block_templ.go`
- Modify: `cmd/breezyd/ui/style.css` (add `.sched-input` class)
- Modify: `cmd/breezyd/handlers_ui_write.go` (delete `getUIScheduleEdit`, `scheduleEditFrag`, `scheduleReadFrag`, `emptyScheduleEntry`)
- Modify: `cmd/breezyd/server.go` (remove `GET /ui/devices/{name}/schedule/edit` route)
- Modify: `cmd/breezyd/handlers_ui_write_test.go` (delete `TestUIScheduleGet_Edit_*`)
- Modify: `cmd/breezyd/ui/templates/render_test.go` (delete `ScheduleBlockEdit` tests; update `TestScheduleEditRow_DeleteButton`)
- Modify: `cmd/breezyd/ui/templates/testdata/golden_healthy.html` + `golden_stale.html` (regenerate)

**Acceptance Criteria:**
- [ ] `ScheduleBlockEdit` template is gone. `ScheduleBlock` is the only one.
- [ ] Every row in the rendered template has `<input type="time">`, `<select name="action">`, `<input type="number" name="pct">` — no static `<td>` text values.
- [ ] When `len(s.Entries) == 0` and the schedule details is rendered, the body shows only a `+ add row` button (no "no entries" wall, no header-only table).
- [ ] The `<details>` element has `data-attr:data-edit` keyed on `$detailsOpen.<name>.schedule` so the attribute is `"true"` when open and absent when closed.
- [ ] Each row input fires `data-on:change__debounce.200ms={ scheduleSubmitExpr(name) }`.
- [ ] The `+ add row` button is positioned below the table (its own row) and does not autosave.
- [ ] The `×` delete button removes the row and triggers a `change` event so the autosave runs.
- [ ] `.sched-input` class renders inputs borderless with dotted hover-underline + focus border (matches `.val-input`).
- [ ] `GET /ui/devices/{name}/schedule/edit` returns 404 (route removed).
- [ ] All Go tests pass: `go test ./...`.
- [ ] Lint clean: `just test-staticcheck`.
- [ ] Goldens regenerated and the diff is just the markup transformation.

**Verify:** `just check` → all green

**Steps:**

- [ ] **Step 1: Rewrite `cmd/breezyd/ui/templates/schedule_block.templ`.**

Replace the entire file with:

```templ
package templates

import (
	"fmt"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/starfederation/datastar-go/datastar"
)

// ScheduleBlock renders the always-editable SCHEDULE <details> block.
// Row fields autosave on change; the + add row button below the table
// appends a new client-side row (no autosave until a field commits).
//
// data-edit="true" while [open] tells the push pipeline's
// [data-block="schedule"]:not([data-edit]) selector to skip patches
// for the duration of the user's interaction. Patches resume the
// next time the user collapses the details. The next-event indicator
// on the summary is hidden while expanded (existing data-show), so
// the user doesn't see it go stale.
templ ScheduleBlock(name string, s ui.ScheduleView, stale bool) {
	if s.Present {
		<details
			id={ "schedule-" + name }
			class={ "block", "schedule", templ.KV("alert", s.Alert) }
			data-block="schedule"
			data-attr:open={ detailsOpenBinding(name, "schedule") }
			data-attr:data-edit={ fmt.Sprintf("$detailsOpen.%s.schedule ? 'true' : null", name) }
		>
			<summary data-on:click={ detailsOpenToggle(name, "schedule") }>
				<h3>SCHEDULE</h3>
				if s.Enabled && s.NextEvent != "" {
					<span class="next-event" data-show={ fmt.Sprintf("!$detailsOpen.%s.schedule", name) }>next event: { s.NextEvent }</span>
				}
			</summary>
			<form>
				<div class="schedule-toolbar">
					<label>
						<input
							type="checkbox"
							name="enabled"
							value="true"
							if s.Enabled { checked }
							data-on:change={ scheduleSubmitExpr(name) }
							if stale || len(s.Entries) == 0 { disabled }
						/>
						<input type="hidden" name="enabled" value="false"/>
						enabled
					</label>
				</div>
				if len(s.Entries) > 0 {
					<table class="schedule-table">
						<thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
						<tbody class="schedule-edit-tbody">
							for _, e := range s.Entries {
								@ScheduleRow(name, e)
							}
						</tbody>
					</table>
				}
				<div class="schedule-addrow">
					<button
						type="button"
						class="btn-inline"
						data-on:click={ scheduleAddRowExpr(name) }
						if stale { disabled }
					>+ add row</button>
				</div>
			</form>
			if s.Alert && s.LastApply != nil {
				<div class="warn">
					{ fmt.Sprintf("⚠ last apply %s failed: %s", s.LastApply.At, s.LastApply.Err) }
					if s.LastApply.Retries > 0 {
						<br/>
						{ fmt.Sprintf("retried %d times", s.LastApply.Retries) }
					}
				</div>
			}
		</details>
	}
}

// ScheduleRow renders one editable row. The inputs all call
// scheduleSubmitExpr on change so the form autosaves whenever the
// user commits a field. Exported so getUIScheduleNewRow can render
// it standalone (the SSE-append handler for the + add row button).
templ ScheduleRow(name string, e ui.ScheduleEntryView) {
	<tr>
		<td>
			<input
				type="time"
				name="at"
				class="sched-input"
				value={ e.At }
				required
				data-on:change__debounce.200ms={ scheduleSubmitExpr(name) }
			/>
		</td>
		<td>
			<select
				name="action"
				class="sched-input"
				data-on:change={ scheduleActionChangeExpr(name) }
			>
				@scheduleActionOption("ventilation", "auto", e.Action)
				@scheduleActionOption("regeneration", "regen", e.Action)
				@scheduleActionOption("supply", "supply", e.Action)
				@scheduleActionOption("extract", "exhaust", e.Action)
				@scheduleActionOption("off", "off", e.Action)
			</select>
		</td>
		<td>
			<input
				type="number"
				name="pct"
				class="sched-input"
				min="10"
				max="100"
				value={ schedulePctValue(e) }
				data-orig-pct={ schedulePctOrigValue(e) }
				data-on:change__debounce.200ms={ schedulePctChangeExpr(name) }
				if e.Action == "off" { readonly }
			/>
		</td>
		<td>
			<button
				type="button"
				class="del"
				data-on:click={ scheduleDelRowExpr(name) }
			>×</button>
		</td>
	</tr>
}

templ scheduleActionOption(value, label, current string) {
	<option value={ value } if value == current { selected }>{ label }</option>
}

// scheduleSubmitExpr builds the data-on:change expression that PUTs
// the form. Wraps datastar.PutSSE for URL formatting and injects
// {contentType: 'form'} so the form fields land in the handler's
// r.Form rather than as a JSON signal-store body.
func scheduleSubmitExpr(name string) string {
	return withDatastarOpts(datastar.PutSSE("/ui/devices/%s/schedule", name), "{contentType: 'form'}")
}

// schedulePctChangeExpr is the pct input's change handler: refresh
// data-orig-pct (so an off→on toggle restores the user's last-edited
// value, not the server-render default) and then run the autosave
// submit.
func schedulePctChangeExpr(name string) string {
	return "evt.target.dataset.origPct = evt.target.value; " + scheduleSubmitExpr(name)
}

// scheduleActionChangeExpr toggles the pct input's readonly/class
// state in lockstep with the action select, then runs the autosave.
// off → clear pct + lock; non-off → restore data-orig-pct + unlock.
func scheduleActionChangeExpr(name string) string {
	return `const pct = evt.target.closest('tr').querySelector('input[name=pct]'); ` +
		`if (evt.target.value === 'off') { pct.value = ''; pct.setAttribute('readonly', ''); } ` +
		`else { pct.value = pct.dataset.origPct; pct.removeAttribute('readonly'); } ` +
		scheduleSubmitExpr(name)
}

// scheduleAddRowExpr re-enables the form's "enabled" checkbox (which
// scheduleDelRowExpr disables when removing the last row) and then
// issues the SSE GET that appends a fresh row. The new row appears
// in the DOM but no autosave fires — the row commits to the server
// when the user edits any field on it.
func scheduleAddRowExpr(name string) string {
	return "var cb = evt.target.closest('form').querySelector('input[type=checkbox][name=enabled]'); cb.disabled = false; " +
		datastar.GetSSE("/ui/devices/%s/schedule/new-row", name)
}

// scheduleDelRowExpr removes the row and, when it was the last one,
// unticks + disables the form's enabled checkbox so the user sees
// the forced-off invariant immediately. Then it triggers an
// autosave so the server state matches the visible DOM (rather
// than waiting for the user's next field edit on a different row).
func scheduleDelRowExpr(name string) string {
	return "var tr = evt.target.closest('tr'); var form = tr.closest('form'); tr.remove(); " +
		"if (form.querySelectorAll('tbody tr').length === 0) { " +
		"var cb = form.querySelector('input[type=checkbox][name=enabled]'); " +
		"cb.checked = false; cb.disabled = true; } " +
		scheduleSubmitExpr(name)
}

// schedulePctValue returns the value attribute for the pct input.
// Empty when Action=="off" so a row that's off doesn't carry a stale
// percent (the user can't act on it, and the handler treats off rows'
// pct as irrelevant — see handlers_ui_write.go).
func schedulePctValue(e ui.ScheduleEntryView) string {
	if e.Action == "off" {
		return ""
	}
	return fmt.Sprintf("%d", e.Pct)
}

// schedulePctOrigValue is the INITIAL value to restore into the pct
// <input> when the user toggles action away from "off". The pct input
// also carries a handler that updates data-orig-pct on every commit,
// so subsequent off→on toggles restore the user's last-edited value
// rather than the server-render original. Pct's valid range is
// [10..100]; out-of-range is the "no value" sentinel and falls back
// to a sensible default of 50.
func schedulePctOrigValue(e ui.ScheduleEntryView) string {
	if e.Pct >= 10 && e.Pct <= 100 {
		return fmt.Sprintf("%d", e.Pct)
	}
	return "50"
}

// scheduleActionLabel maps action values to display labels. Still
// used by the snapshot-view label conversion outside templ.
func scheduleActionLabel(action string) string {
	switch action {
	case "ventilation":
		return "auto"
	case "regeneration":
		return "regen"
	case "supply":
		return "supply"
	case "extract":
		return "exhaust"
	case "off":
		return "off"
	default:
		return action
	}
}
```

- [ ] **Step 2: Update `getUIScheduleNewRow` to call the renamed template.**

In `cmd/breezyd/handlers_ui_write.go`, find `getUIScheduleNewRow`. Change `templates.ScheduleEditRow(...)` to `templates.ScheduleRow(name, ...)`:

```go
	if err := sse.PatchElementTempl(
		templates.ScheduleRow(name, emptyScheduleEntry()),
		datastar.WithSelectorf(`.card[data-device=%q] tbody.schedule-edit-tbody`, name),
		datastar.WithMode(datastar.ElementPatchModeAppend),
	); err != nil {
```

Keep `emptyScheduleEntry()` for now — `getUIScheduleNewRow` still uses it. (It'll be inlined or kept as the canonical default.)

- [ ] **Step 3: Delete `getUIScheduleEdit`, `scheduleEditFrag`, `scheduleReadFrag`, and `scheduleAcknowledgeSSE`'s old-name caller.**

In `cmd/breezyd/handlers_ui_write.go`:

1. Delete the `getUIScheduleEdit` function.
2. Delete the `scheduleEditFrag` function and update `putUISchedule`'s validation-error paths to call `errorBannerSSE(w, r, http.StatusUnprocessableEntity, msg)` instead. The existing `errorBannerSSE` already handles `Datastar-Status` and the patch into `#global-error-banner`. Find every call to `h.scheduleEditFrag(w, r, name, "...")` in `putUISchedule` and replace with `errorBannerSSE(w, r, http.StatusUnprocessableEntity, "...")`.
3. Delete the `scheduleReadFrag` function (no longer called — the empty-SSE acknowledge from Task 1 is the success path).
4. Remove the `scheduleSelector` function if it's only used by the deleted helpers.

- [ ] **Step 4: Remove the `GET /ui/devices/{name}/schedule/edit` route.**

In `cmd/breezyd/server.go`, find and delete the line:

```go
	mux.HandleFunc("GET /ui/devices/{name}/schedule/edit", h.getUIScheduleEdit)
```

The `GET /ui/devices/{name}/schedule` (read) and `PUT /ui/devices/{name}/schedule` (autosave) routes stay. `GET /ui/devices/{name}/schedule/new-row` stays.

- [ ] **Step 5: Update `getUIScheduleRead` to use the new template.**

In `cmd/breezyd/handlers_ui_write.go`, `getUIScheduleRead` currently calls `h.scheduleReadFrag` which is being deleted. Inline the patch directly:

```go
func (h *Handler) getUIScheduleRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	patchFragmentSSE(w, r, scheduleSelector(name), templates.ScheduleBlock(name, view.Schedule, view.Stale))
}
```

Keep `scheduleSelector` in the file — `getUIScheduleRead` uses it. (Move the function into `handlers_ui_write.go` near `getUIScheduleRead` if its prior location was tied to one of the deleted helpers.)

- [ ] **Step 6: Delete the `TestUIScheduleGet_Edit_*` tests.**

In `cmd/breezyd/handlers_ui_write_test.go`, delete:
- `TestUIScheduleGet_Edit`
- `TestUIScheduleGet_Edit_EmptySchedule`
- `TestUIScheduleGet_Edit_NotFound`

These tests covered the removed endpoint. The remaining `TestUIScheduleGet_Read*`, `TestUISchedulePut_*`, `TestUIScheduleGet_NewRow*`, `TestPostUISchedEnabled_*` cover everything still wired.

Update `TestUIScheduleGet_Read` to assert the new template's hallmarks — editable inputs, no static `<td>` text:

```go
func TestUIScheduleGet_Read(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, `class="block schedule"`)) // body has schedule block
	// Empty schedule renders the + button but no row inputs.
	is.True(strings.Contains(bs, `+ add row`))
	is.True(!strings.Contains(bs, `name="at"`))
}
```

- [ ] **Step 7: Update / delete templ render tests.**

In `cmd/breezyd/ui/templates/render_test.go`:

1. Find any tests that render `ScheduleBlockEdit` — delete them.
2. Update `TestScheduleEditRow_DeleteButton` (rename to `TestScheduleRow_DeleteButton`) to render via `ScheduleRow("alpha", entry)` and assert the new del expression structure: still removes the row + manipulates checkbox, plus now triggers a PUT.
3. Add a new test `TestRenderScheduleBlock_AlwaysEditable` that renders `ScheduleBlock("alpha", schedule-with-one-entry, false)` and asserts the row contains `<input type="time"`, `<select name="action"`, `<input type="number" name="pct"`.
4. Add `TestRenderScheduleBlock_Empty` that renders with `len(Entries)==0` and asserts the body contains `+ add row` but no `name="at"` / `name="action"` strings.

- [ ] **Step 8: Add the `.sched-input` CSS class.**

In `cmd/breezyd/ui/style.css`, near the `.val-input` block, add:

```css
/* Inline-editable schedule row inputs. Borderless against the row
   background so each field reads as text; dotted underline on hover
   signals click-and-type; thin border on focus shows which field is
   active. Mirrors the .val-input pattern from v2.0.2 but applied to
   time / select / number inputs in the schedule table. */
.sched-input {
  font: inherit; color: inherit;
  background: transparent;
  border: 1px solid transparent; border-radius: 3px;
  padding: 0 0.2rem; margin: 0;
}
.sched-input:hover:not(:disabled):not(:focus):not([readonly]) {
  cursor: pointer;
  text-decoration: underline;
  text-decoration-style: dotted;
  text-underline-offset: 2px;
}
.sched-input:focus {
  outline: none;
  border-color: var(--border-input);
}
.sched-input:disabled,
.sched-input[readonly] { opacity: 0.5; cursor: not-allowed; }

/* Right-side + button sits on its own row below the table. */
.schedule-addrow { margin-top: 0.4rem; }
```

- [ ] **Step 9: Regenerate templ + goldens.**

```sh
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
```

Read the golden diff before continuing — it should show only the markup transformation (new `<form>`/`<input>` elements, removed `<details data-edit="true">` from the edit variant, etc.). If it shows anything surprising, stop and re-check.

- [ ] **Step 10: Run the full Go test suite + lint.**

```sh
just check
```

Expected: all green. If `TestUIScheduleGet_Read` or `TestPostUISchedEnabled_*` fail because the new template's shape differs from what they assert, adjust the assertions to match the new shape (preserving the underlying semantic invariant).

- [ ] **Step 11: Commit.**

```sh
git add cmd/breezyd/ui/templates/schedule_block.templ \
        cmd/breezyd/ui/templates/schedule_block_templ.go \
        cmd/breezyd/ui/templates/render_test.go \
        cmd/breezyd/ui/templates/testdata/golden_healthy.html \
        cmd/breezyd/ui/templates/testdata/golden_stale.html \
        cmd/breezyd/ui/style.css \
        cmd/breezyd/handlers_ui_write.go \
        cmd/breezyd/handlers_ui_write_test.go \
        cmd/breezyd/server.go
git commit -m "feat(ui): inline-edit schedule, single template, autosave on change

Collapses the read-variant + edit-variant split into one always-
editable ScheduleBlock. Row inputs autosave on change via the
existing PUT endpoint; + button appends a fresh row client-side
(no autosave until a field is committed); × deletes the row and
triggers an autosave so the form state matches the DOM.

data-edit=\"true\" while [open] makes the push pipeline filter
schedule patches for the duration of the user's interaction —
mid-typed inputs survive polls; patches resume on collapse.

Deletes GET /ui/devices/{name}/schedule/edit, scheduleEditFrag,
scheduleReadFrag. validation failures route through errorBannerSSE
(same banner pattern as every other action handler).

See docs/superpowers/specs/2026-05-11-schedule-inline-edit-design.md
for the full design.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

### Task 3: Rewrite Playwright tests for inline-edit flow

**Goal:** Replace mode-toggle-based Playwright tests with inline-edit equivalents. Pin three behaviors that didn't exist before: field-edit → PUT, + button without autosave, data-edit filter while details is open.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts` (the `schedule editor` describe block and the editor-preservation test)

**Acceptance Criteria:**
- [ ] No reference to `edit schedule` / `save` / `cancel` buttons anywhere in `dashboard.spec.ts`.
- [ ] Inline-edit happy path: focus pct input, fill, blur, assert one `PUT /ui/devices/{name}/schedule` request with form-encoded body containing the new pct.
- [ ] `+ add row` adds a `<tr>` to the table; the test asserts **no** PUT fires until a field on the new row is committed.
- [ ] Editor preservation across polls (#65): the inline-edit form's `data-edit="true"` (driven by `$detailsOpen`) keeps the schedule block patches filtered while the details is open. Test asserts the typed pct survives N poll cycles.
- [ ] `just test-ui` passes all 24 tests.

**Verify:** `just test-ui` → 24 passed

**Steps:**

- [ ] **Step 1: Rewrite the `schedule editor` describe block.**

In `tests/ui/dashboard.spec.ts`, find:

```ts
test.describe("schedule editor", () => {
  test("edit → modify pct → save persists the entry", ...
  test("edit on empty schedule auto-seeds a row", ...
})
```

Replace with:

```ts
test.describe("schedule editor", () => {
  // Pins the inline-edit autosave path. Focus a pct cell, type a new
  // value, blur — exactly one PUT goes out and the daemon's schedule
  // snapshot reflects the new value.
  test("edit pct field → autosave PUT persists the value", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });
    const card = await loadCard(page);
    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');
    await expect(details).toHaveAttribute("data-edit", "true", { timeout: 2_000 });

    const pctInput = details.locator('input[name="pct"]');
    await expect(pctInput).toHaveCount(1);

    const putRequests: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PUT" && req.url().endsWith(`/ui/devices/${DEVICE}/schedule`)) {
        putRequests.push(req.postData() || "");
      }
    });

    await pctInput.fill("77");
    await pctInput.blur();

    await expect.poll(() => putRequests.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(putRequests[0]).toContain("pct=77");
  });

  // Pins #6 (the previous auto-seed) replacement: clicking + adds a
  // client-side row, but no PUT fires until the user edits a field.
  // Avoids creating phantom 08:00 entries on misclicks.
  test("+ add row adds a tr but no PUT until field commit", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, { enabled: false, entries: [] });
    const card = await loadCard(page);
    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');

    const putRequests: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PUT" && req.url().endsWith(`/ui/devices/${DEVICE}/schedule`)) {
        putRequests.push(req.url());
      }
    });

    await card.getByRole("button", { name: "+ add row" }).click();
    // After the SSE patch lands, exactly one row exists in the table body.
    await expect(details.locator('tbody.schedule-edit-tbody tr')).toHaveCount(1, {
      timeout: POLL_PUSH_TIMEOUT,
    });

    // Give a generous window for an unwanted autosave to fire. None should.
    await page.waitForTimeout(500);
    expect(putRequests).toHaveLength(0);
  });
});
```

- [ ] **Step 2: Update the editor-preservation test (#65).**

In `tests/ui/dashboard.spec.ts`, find the test `schedule editor survives multiple polls with typed pct intact` inside `editor preservation across polls (#65)`. The setup no longer involves an "edit schedule" button — the inputs are always live. Rewrite:

```ts
  test("schedule editor survives multiple polls with typed pct intact", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });
    const card = await loadCard(page);

    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');
    await expect(details).toHaveAttribute("data-edit", "true", { timeout: 2_000 });

    const pctInput = details.locator('input[name="pct"]');
    await expect(pctInput).toHaveCount(1);
    await pctInput.fill("77");
    // Don't blur yet — we want to assert the typed value survives polls
    // while the input still has focus (the autosave hasn't fired yet,
    // and data-edit="true" filters incoming patches).

    await assertStableAcrossPolls(page, async () => {
      await expect(details).toBeVisible({ timeout: 200 });
      await expect(pctInput).toHaveValue("77", { timeout: 200 });
    });
  });
```

- [ ] **Step 3: Remove the now-redundant `edit on empty schedule auto-seeds a row` test.**

That behavior was specific to the old edit-mode entry. With inline-edit, the equivalent is the `+ add row → no PUT until commit` test added in Step 1. Delete the old test if it still exists after Step 1's rewrite.

- [ ] **Step 4: Run the UI suite.**

```sh
just test-ui
```

Expected: 24/24 passing. Investigate any failures inline — flakes are bugs per the project memory, never re-run.

- [ ] **Step 5: Commit.**

```sh
git add tests/ui/dashboard.spec.ts
git commit -m "test(ui): rewrite schedule editor Playwright tests for inline-edit

- edit pct field → autosave PUT (was: edit → modify → save click)
- + add row adds a tr but no PUT until field commit (was: edit on
  empty schedule auto-seeds a row)
- editor preservation #65: now driven by details[open]→data-edit
  filter rather than the lifecycle-scoped edit form

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
```

---

## Self-review notes

- Spec coverage: every section of the spec (autosave, banner errors, empty-state UX, data-edit filter, deleted endpoints, test plan, migration) maps to at least one task.
- Type consistency: `ScheduleRow` is consistently named across the templ rewrite, the `getUIScheduleNewRow` patch, and the render-test updates.
- Decomposition: Task 1 is the safest preliminary (handler-only, leaves the old template wired); Task 2 is the bulk feature change; Task 3 is the e2e harness update. Each task ends in a green test run + a commit.

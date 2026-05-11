# Schedule inline-edit redesign

**Status:** draft, awaiting user review
**Date:** 2026-05-11
**Replaces:** the two-mode (read-variant + edit-variant) schedule UX shipped in v1.4 / refined through v2.0.2.

## Problem

The current schedule block has a "view mode" with static rows and an explicit "edit schedule" button that fetches an "edit mode" fragment via SSE. The edit fragment ships its own form, save/cancel buttons, per-row inputs, and a `+ add row` control. Saving issues a `PUT` of the whole form, which returns the read fragment.

The mode toggle is a weak affordance:

- Two clicks to change one field (`edit schedule` → click the field).
- The user must remember to hit "save" or risk losing all edits to "cancel".
- The two fragments duplicate markup (read row vs. edit row) and template logic.
- "Cancel" is a destructive operation with no preview of what would be discarded.

## Goal

Collapse to a single always-editable view:

- Row fields are inline-editable. Clicking a field jumps straight into the native input — no mode toggle, no form chrome.
- A small `+ add row` button below the table appends a row.
- Changes auto-save to the server on each field commit (`change` event).
- The existing forced-off invariant (empty entries → enabled=false) and the next-event indicator stay as-is.

## Non-goals

- No change to the daemon's `Scheduler` behavior, the `PUT /v1/devices/{name}/schedule` JSON API, or the `PUT /ui/devices/{name}/schedule` form endpoint.
- No change to validation rules: at-time format, action enum, pct range, duplicate-at detection.
- No change to schedule firing, retry, or persistence semantics.
- No undo/cancel: every committed field change writes through immediately. Users who want to discard a change re-edit the field.

## UX

### Layout (expanded)

```
SCHEDULE                                  next event: 08:00
[✓] enabled

  at      mode     fan
  08:00   regen    60%        ×
  18:00   off      —          ×

  + add row
```

The `next event: HH:MM` indicator on the summary is unchanged — visible only while the block is collapsed (existing `data-show` on the summary span).

### Field styling

All three input types — `<input type="time">`, `<select name="action">`, `<input type="number" name="pct">` — render borderless against the row background, with:

- Dotted underline on hover (matches the `.val-input` pattern from v2.0.2).
- Thin solid border on `:focus` so the user sees which field they're editing.
- Disabled appearance unchanged when stale (existing behavior).

A shared `.sched-input` class carries the styling. Browser-native widgets (time picker popover, select dropdown) are still triggered by clicking the field — only the resting visual is text-like.

### Saving

Every input's `data-on:change__debounce.200ms` posts a `PUT` of the entire form to `/ui/devices/{name}/schedule` with `{contentType: 'form'}`. The debounce coalesces a tab-through-multiple-fields gesture into one PUT. The server validates the whole schedule atomically (same path as today's edit-form submit).

**Success path:** server returns `200 OK` with an empty SSE event stream — no patch. The form's DOM state is already correct because the user just typed it; sending back a re-rendered fragment would clobber whatever input still has focus. The next regular poll cycle repaints the block (after the user collapses or moves on).

**Failure path:** server returns `200 OK` with a `datastar-patch-elements` event into `#global-error-banner` carrying the validation message (existing `errorBannerSSE` pattern). The user reads the banner, fixes the field, the next change retries.

The `Datastar-Status` header carries the semantic 4xx for observability — unchanged from today.

### Row delete (`×`)

Per-row delete button stays. Click:

1. Removes the row from the DOM client-side (`tr.remove()`).
2. Fires a synthetic `change` event on the form so the autosave runs without that row.
3. If this was the last row, the existing forced-off logic still applies — server-side `Replace(enabled=anything, entries=[])` coerces `enabled→false`; the read-variant render then shows the checkbox disabled.

### Row add (`+`)

`+ add row` button lives below the table on its own row, styled as a small inline button (`.btn-inline` or similar). Click:

1. Fetches a fresh row via `GET /ui/devices/{name}/schedule/new-row` (existing endpoint, unchanged).
2. Server returns the row HTML as an SSE patch with mode=append targeting the form's `tbody`.
3. **Does NOT trigger an autosave.** The new row's defaults (08:00 / regeneration / 60) are not committed to the server until the user changes at least one field. This avoids creating a phantom 08:00 entry on a stray misclick — the user has to "claim" the row by editing one of its fields.

Implementation note: the row's default values are still passed to the server when *any* field on that row is later edited (autosave PUTs the whole form). So a user who clicks `+`, then changes the time to 09:00 but leaves mode/pct at the defaults, ends up with a 09:00/regen/60 entry. Same as today's edit-mode behavior.

### Empty state

When `len(entries) == 0` and the user hasn't yet clicked `+`:

- No "no entries" text.
- Just the `+ add row` button at the bottom of the otherwise empty (or table-header-only) area.
- The `enabled` checkbox is disabled (existing forced-off invariant).

### Patch filtering while editing

The schedule block carries `data-edit="true"` whenever its `<details>` is open. The push pipeline's selector `[data-block="schedule"]:not([data-edit])` (in `push_render.go`) already filters such patches, so:

- **Expanded:** no SSE-driven repaints of the block at all. The user's edits never get clobbered mid-type. The next-event indicator is hidden anyway (`data-show="!$detailsOpen"`).
- **Collapsed:** patches resume. The next poll repaints to reflect any state drift (next-event recomputed, alert flag updated, etc.).

Implementation: `data-attr:data-edit={ "$detailsOpen.<name>.schedule ? 'true' : null" }` on the `<details>` itself.

This is a coarser filter than what the edit-mode `data-edit="true"` did before (which was lifecycle-scoped to the edit form). The coarser filter is acceptable because (a) schedule state changes infrequently, (b) the only currently-dynamic surface visible while expanded is per-row data and the alert warn-line — both of which the user explicitly drove via the inline edits.

## Code changes

### Removed

- `ScheduleBlockEdit` template (and its renderer call sites).
- `scheduleReadRow` template — replaced by `ScheduleEditRow` (folded into a single shared row template, renamed `scheduleRow` since there's no longer a read/edit distinction).
- `getUIScheduleEdit` handler + the `/ui/devices/{name}/schedule/edit` route registration. Tests `TestUIScheduleGet_Edit*` go with it.
- `scheduleEditFrag` helper, except its "auto-seed an empty row when editing-an-empty-schedule" logic — moved into the spec's section on empty state. The new design's `+ add row` handles the "user wants a row" intent explicitly.

### Modified

- `ScheduleBlock` becomes the single template. It renders the `<form>` + `<table>` + `+ add row` button when there are entries, or just the `+ add row` button below the (header-only or absent) table when there are none. Inline-editable fields are always live.
- `scheduleSubmitExpr` is **kept** and now called from each input's `data-on:change__debounce.200ms` rather than from a single `data-on:submit__prevent` on the `<form>`. The `<form>` itself stays so `contentType: 'form'` has a target for value scraping; the implicit submit event is no longer used (there's no submit button).
- `putUISchedule` on success: switch from "return read fragment via `scheduleReadFrag`" to "return empty SSE event stream" (`200 OK` + the SSE Content-Type but no `event:` lines). Existing error paths (`scheduleEditFrag(err)`) become `errorBannerSSE(...)` calls — the banner pattern every other action handler already uses. The `Datastar-Status` header keeps the 422.
- `scheduleAddRowExpr` keeps the re-enable-checkbox prefix; the SSE-GET call is unchanged.
- `scheduleDelRowExpr` keeps the row-remove + last-row-disables-checkbox logic and additionally calls `scheduleSubmitExpr(name)` as a JS statement so the autosave fires immediately after the row is removed.

### Added

- `.sched-input` CSS class for the borderless / hover-underline / focus-border treatment, applied to the `<input type="time">`, `<select>`, and `<input type="number">` elements.

### Unchanged

- `Scheduler.SetEnabled`, `Scheduler.Replace`, all validation rules.
- `PUT /v1/devices/{name}/schedule` JSON endpoint and its tests.
- `getUIScheduleNewRow` handler — still returns the SSE patch with the default row.
- The `next-event` indicator, the alert/warn line, the `enabled` checkbox in the toolbar.
- The collapsible `<details>` outer chrome.

## Testing

Replace the two-mode tests with single-mode equivalents:

- `TestUIScheduleGet_Read` — adjust to assert the rendered template has the editable inputs (since there's no longer a "read variant" with static text). Test name stays for continuity.
- `TestUIScheduleGet_Edit*` tests (incl. `TestUIScheduleGet_Edit_EmptySchedule` added in v2.0.2) — delete; the endpoint is gone and the auto-seed behavior is replaced by the user clicking `+`.
- `TestUISchedulePut_Happy` — assert the response body is an SSE event stream with **no** `event: datastar-patch-elements` lines (vs. today's "contains the read fragment"). Add a positive check that no fragment was emitted on success.
- New: `TestRenderScheduleBlock_AlwaysEditable` — render with one entry and assert the row contains `<input type="time">`, `<select name="action">`, and `<input type="number" name="pct">`. Render with zero entries and assert the `+ add row` button is present.
- Playwright `schedule editor edit → modify pct → save persists the entry` — rewrite as: focus the pct input directly, fill `"77"`, blur, assert the row's stored pct is 77 by re-reading via the daemon's `GET /v1/devices/{name}/schedule` (or polling the cache). No "edit schedule" / "save" buttons to click.
- Playwright `edit on empty schedule auto-seeds a row` — rewrite as: click `+ add row` on a card with no schedule entries, assert one row appears in the DOM, assert no `PUT /ui/devices/{name}/schedule` request was sent yet (the `+` is row-add only, not row-commit).
- Playwright new: `editing a field triggers an autosave PUT` — fill a row's pct field, blur, observe one `PUT` request with the form-encoded body containing the new pct.

The existing schedule-editor-preservation-across-polls test (#65) becomes mostly moot because the whole block is `data-edit="true"` whenever the details is open; the patch can't land at all. The test is kept (assertion: typed pct stays put across N polls) but its mechanism is now "block-level data-edit filter" rather than the lifecycle-scoped edit-form variant.

## Migration

This is a breaking UX change with no backwards compatibility shim — there's only ever one client (the dashboard) and one user. Lands in a single release. Update CHANGELOG.md under `[Unreleased]` with a `### Changed` entry explaining the mode-toggle removal.

## Open questions

None — see "Picking defaults" in the brainstorming exchange for the two earlier decisions.

# `breezy param` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global `breezy param` verb that prints the static parameter registry from `pkg/breezy/params.go` as a wide ASCII table, so users discovering names for `get`/`set` can list them from the CLI.

**Architecture:** Two commits. (1) Pure renderer + capability-letter helper in `cmd/breezy/render.go`, plus unit test in `cmd/breezy/main_test.go` (the project's existing convention — `render.go` has no separate test file). (2) Wire-up: new `cmdParam` in `commands.go`, `case "param":` in `main.go`'s globals switch, `usage` line, README + CLAUDE.md mentions, and a black-box CLI test. No daemon contact: pure metadata read from `breezy.AllParams()`.

**Tech Stack:** Go 1.22+, `pkg/breezy` registry, no new deps.

**Spec:** `docs/superpowers/specs/2026-05-04-param-list-design.md`
**Issue:** [#3 — Params Table](https://github.com/hughobrien/breezyd/issues/3)

---

### Task 1: `renderParams` + `capsString` helper, with unit test

**Goal:** Pure formatter that turns `[]breezy.Param` into the wide table from the spec. Independently testable, no I/O outside the supplied `io.Writer`. After this task, `renderParams` is importable from anywhere in `cmd/breezy/` and its formatting is locked in by tests, but no CLI verb exposes it yet.

**Files:**
- Modify: `cmd/breezy/render.go` (append at end of file — keep package import set)
- Modify: `cmd/breezy/main_test.go` (append two new test functions)

**Acceptance Criteria:**
- [ ] `renderParams(w io.Writer, params []breezy.Param)` exists in `cmd/breezy/render.go`.
- [ ] `capsString(c breezy.Capabilities) string` helper exists in the same file.
- [ ] Header row is `ID  NAME  TYPE  UNIT  CAPS  DESCRIPTION` (uppercase, two-space gutters).
- [ ] IDs render as 4-digit hex with `0x` prefix and uppercase A-F (e.g. `0x0044`, `0x004A`) — matches existing project convention (`commands.go`, `params.go` panic messages already use `%04X`).
- [ ] `Param.Type.String()` is used directly for the TYPE column.
- [ ] Empty `Param.Unit` renders as `-`.
- [ ] CAPS letters appear in fixed order `R W I D`, concatenated, no separators.
- [ ] Column widths are computed dynamically from the rendered cells (per-column max), separated by two spaces, last column unpadded.
- [ ] `TestRenderParams` passes.
- [ ] `TestCapsString` passes.

**Verify:** `go test ./cmd/breezy -run 'TestRenderParams|TestCapsString' -v` → both PASS.

**Steps:**

- [ ] **Step 1: Write the failing tests**

Append to `cmd/breezy/main_test.go` (these tests reference symbols that don't exist yet, so the whole package will fail to compile until Step 3):

```go
func TestCapsString(t *testing.T) {
	for _, tc := range []struct {
		caps breezy.Capabilities
		want string
	}{
		{breezy.CapRead, "R"},
		{breezy.CapWrite, "W"},
		{breezy.CapReadWrite, "RW"},
		{breezy.CapAll, "RWID"},
		{breezy.CapRead | breezy.CapInc, "RI"},
	} {
		if got := capsString(tc.caps); got != tc.want {
			t.Errorf("capsString(%b) = %q, want %q", tc.caps, got, tc.want)
		}
	}
}

func TestRenderParams(t *testing.T) {
	params := []breezy.Param{
		{ID: 0x0001, Name: "power", Type: breezy.TypeUint8, Unit: "", Caps: breezy.CapAll, Description: "Turn on/off"},
		{ID: 0x004A, Name: "fan_supply_rpm", Type: breezy.TypeUint16, Unit: "rpm", Caps: breezy.CapRead, Description: "Live RPM"},
		{ID: 0x0065, Name: "reset_filter_timer", Type: breezy.TypeWriteOnly, Unit: "", Caps: breezy.CapWrite, Description: "Trigger"},
	}
	var buf bytes.Buffer
	renderParams(&buf, params)
	out := buf.String()

	for _, sub := range []string{
		"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION",
		"0x0001", "power", "uint8", "RWID", "Turn on/off",
		"0x004A", "fan_supply_rpm", "uint16", "rpm", "Live RPM",
		"0x0065", "reset_filter_timer", "write_only", "W", "Trigger",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("renderParams output missing %q:\n%s", sub, out)
		}
	}

	// Empty Unit must render as "-".
	powerLine := ""
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, "0x0001") {
			powerLine = line
			break
		}
	}
	if powerLine == "" {
		t.Fatalf("no row found for 0x0001 in output:\n%s", out)
	}
	// The UNIT column for power must be the literal "-" (surrounded by spaces).
	if !strings.Contains(powerLine, " -  ") {
		t.Errorf("expected empty Unit rendered as '-' in row, got:\n%s", powerLine)
	}

	// Header line is the first non-empty line.
	firstLine := strings.SplitN(out, "\n", 2)[0]
	wantHeaderOrder := []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"}
	prev := -1
	for _, h := range wantHeaderOrder {
		idx := strings.Index(firstLine, h)
		if idx < 0 {
			t.Fatalf("header missing %q: %q", h, firstLine)
		}
		if idx <= prev {
			t.Fatalf("header column %q out of order in: %q", h, firstLine)
		}
		prev = idx
	}
}
```

The existing `cmd/breezy/main_test.go` already imports `bytes`, `strings`, and `testing`. Add `"github.com/hughobrien/breezyd/pkg/breezy"` to its import block if not already present (it's used in existing tests like `TestSetRejectReadOnly`, so it should be there — verify with `grep '"github.com/hughobrien/breezyd/pkg/breezy"' cmd/breezy/main_test.go`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/breezy -run 'TestRenderParams|TestCapsString' -v`
Expected: FAIL — `undefined: capsString` and `undefined: renderParams` (compile errors are fine; the tests will run once the implementation lands).

- [ ] **Step 3: Implement `capsString` + `renderParams` in `cmd/breezy/render.go`**

Append to `cmd/breezy/render.go`. The existing imports (`fmt`, `io`, `sort`, `strings`, `time`) cover everything needed; add `"github.com/hughobrien/breezyd/pkg/breezy"` to the import block.

```go
// capsString renders a Capabilities bitmask as a fixed-order letter
// concatenation: R, W, I, D. Read-only -> "R", common writable -> "RW",
// fully capable -> "RWID", write-only triggers -> "W".
func capsString(c breezy.Capabilities) string {
	var b strings.Builder
	if c.CanRead() {
		b.WriteByte('R')
	}
	if c.CanWrite() {
		b.WriteByte('W')
	}
	if c.CanInc() {
		b.WriteByte('I')
	}
	if c.CanDec() {
		b.WriteByte('D')
	}
	return b.String()
}

// renderParams writes the parameter-registry table to w. Columns:
// ID (4-digit hex), NAME, TYPE, UNIT (empty -> "-"), CAPS, DESCRIPTION.
// Two-space gutters; last column unpadded. Rows are emitted in input
// order — the caller is expected to sort if it cares (breezy.AllParams
// already returns sorted-by-ID).
func renderParams(w io.Writer, params []breezy.Param) {
	const (
		hID, hName, hType, hUnit, hCaps, hDesc = "ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"
	)
	wID, wName, wType, wUnit, wCaps := len(hID), len(hName), len(hType), len(hUnit), len(hCaps)

	cells := make([][6]string, 0, len(params))
	for _, p := range params {
		idStr := fmt.Sprintf("0x%04X", uint16(p.ID))
		typeStr := p.Type.String()
		unit := p.Unit
		if unit == "" {
			unit = "-"
		}
		caps := capsString(p.Caps)
		row := [6]string{idStr, p.Name, typeStr, unit, caps, p.Description}
		if len(idStr) > wID {
			wID = len(idStr)
		}
		if len(p.Name) > wName {
			wName = len(p.Name)
		}
		if len(typeStr) > wType {
			wType = len(typeStr)
		}
		if len(unit) > wUnit {
			wUnit = len(unit)
		}
		if len(caps) > wCaps {
			wCaps = len(caps)
		}
		cells = append(cells, row)
	}

	fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		wID, hID, wName, hName, wType, hType, wUnit, hUnit, wCaps, hCaps, hDesc)
	for _, r := range cells {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			wID, r[0], wName, r[1], wType, r[2], wUnit, r[3], wCaps, r[4], r[5])
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/breezy -run 'TestRenderParams|TestCapsString' -v`
Expected: PASS for both tests.

- [ ] **Step 5: Run the lint gate**

Run: `just lint`
Expected: exit 0, no gofmt drift.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezy/render.go cmd/breezy/main_test.go
git commit -m "$(cat <<'EOF'
cmd/breezy: renderParams + capsString for the param-list verb

Pure formatter for the parameter registry. Wide ASCII table with
columns ID/NAME/TYPE/UNIT/CAPS/DESCRIPTION, two-space gutters,
empty Unit rendered as "-". Not yet wired to a verb.

Issue: #3
EOF
)"
```

---

### Task 2: Wire the `param` verb into the CLI + docs

**Goal:** Ship `breezy param` end-to-end. Globals switch in `main.go` dispatches to a new `cmdParam`, `usage` lists it, README and CLAUDE.md mention it, and a black-box CLI test asserts the invocation works.

**Files:**
- Modify: `cmd/breezy/commands.go` (append `cmdParam` near the other globals — `cmdLs` / `cmdDiscover`, around line 411 onward)
- Modify: `cmd/breezy/main.go` (add `case "param":` in the globals switch around line 97; add a line under "Globals" in the `usage` const around line 179)
- Modify: `cmd/breezy/main_test.go` (append `TestParam`)
- Modify: `README.md` (add a one-line mention under the CLI surface section)
- Modify: `CLAUDE.md` (add `param` to the globals list under "## CLI surface")

**Acceptance Criteria:**
- [ ] `breezy param` exits 0 and prints a header row plus one row per registered parameter.
- [ ] The output row count equals `len(breezy.AllParams())`.
- [ ] Output contains `0x0001  power` and `0x0044  speed_manual_pct` (spot-check entries).
- [ ] `breezy --help` (or `breezy` with no args) lists `param` under Globals.
- [ ] `TestParam` passes.
- [ ] `just check` passes.
- [ ] README mentions `breezy param` in the CLI surface block.
- [ ] CLAUDE.md lists `param` among the globals.

**Verify:** `just check` exits 0, then `go run ./cmd/breezy param | head -3` prints the header followed by `0x0001  power  uint8  -  RWID  …`.

**Steps:**

- [ ] **Step 1: Write the failing CLI test**

Append to `cmd/breezy/main_test.go`. This test does not need an HTTP server — `param` is a global metadata-only verb, identical in shape to `TestDaemonURL` (which calls `run` directly).

```go
func TestParam(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"param"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()

	// Header.
	for _, h := range []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"} {
		if !strings.Contains(out, h) {
			t.Errorf("missing header %q in output:\n%s", h, out)
		}
	}

	// Spot-check known params.
	for _, sub := range []string{"0x0001", "power", "0x0044", "speed_manual_pct"} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in output:\n%s", sub, out)
		}
	}

	// Row count = registered params + 1 header.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := len(breezy.AllParams()) + 1
	if len(lines) != want {
		t.Errorf("got %d lines, want %d (header + %d params)", len(lines), want, want-1)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/breezy -run TestParam -v`
Expected: FAIL — current `run` falls through to `unknown verb: param` (exit 2). Test asserts exit 0.

- [ ] **Step 3: Add `cmdParam` to `cmd/breezy/commands.go`**

Append to `cmd/breezy/commands.go` (after `cmdDiscover`, end of file). No new imports needed beyond the existing ones — `breezy` and `io` are already imported.

```go
// cmdParam prints the static parameter registry as a wide table. Pure
// metadata read; no daemon round-trip. Exit code is always 0 (the
// registry is built into the binary).
func cmdParam(stdout io.Writer) int {
	renderParams(stdout, breezy.AllParams())
	return 0
}
```

- [ ] **Step 4: Wire dispatch in `cmd/breezy/main.go`**

Add a case to the globals switch (currently around lines 97–111). Insert the new case alphabetically — between `discover` and `daemon-url` would put it before `daemon-url`; the cleanest spot is right after the existing `daemon-url` case so the related globals stay grouped. Use this edit:

```go
	case "daemon-url":
		fmt.Fprintln(stdout, daemonURL)
		return 0
	case "param":
		return cmdParam(stdout)
```

Then update the `usage` const (currently around lines 156–183). In the **Globals** block, add a line below `daemon-url`:

```
  daemon-url            print the URL the CLI would use
  param                 list known parameters (id, type, unit, caps)
```

- [ ] **Step 5: Run the new test + the full fast suite**

Run: `go test ./cmd/breezy -run TestParam -v`
Expected: PASS.

Run: `just check`
Expected: exit 0 (lint + fast tests).

- [ ] **Step 6: Manual smoke test**

Run: `go run ./cmd/breezy param | head -5`
Expected:
```
ID      NAME                    TYPE    UNIT  CAPS  DESCRIPTION
0x0001  power                   uint8   -     RWID  Turn the unit on/off (0=off, 1=on, 2=invert)
0x0002  speed_mode              uint8   -     RWID  Speed preset 1-3 or 255=manual percentage mode
0x0007  timer                   uint8   -     RWID  Active special-mode (0=off, 1=night, 2=turbo)
0x000B  timer_countdown         time_of_day  -  R   Time remaining in active special mode
```
(Exact column widths depend on the longest cell in each column — what matters is that rows line up and the header is present.)

- [ ] **Step 7: Update `README.md`**

Find the CLI surface section. The `get` / `set` paragraph (or the bullet list of verbs) is the right neighbour. Add a single bullet/line at the appropriate indentation level for the existing list:

```
- `breezy param` — list known parameters with type, unit, and capabilities (use the `name` column with `get` / `set`).
```

Read the existing surrounding context first so the indentation and style (bullet vs. fenced block) match what's already there. Use `grep -n "breezy ls\|breezy discover" README.md` to find the exact spot.

- [ ] **Step 8: Update `CLAUDE.md`**

Find the "## CLI surface" section. The current globals are listed as `ls`, `discover`, `daemon-url`. Update to:

`globals (`ls`, `discover`, `daemon-url`, `param`)`

Use `grep -n "ls\`, \`discover\`, \`daemon-url" CLAUDE.md` to find the exact line. The change is one word.

- [ ] **Step 9: Final verification**

Run: `just check`
Expected: exit 0.

Run: `go run ./cmd/breezy param | wc -l`
Expected: `N+1` where `N = len(breezy.AllParams())` (currently 70 → expect 71). Verify by also running:

`go run ./cmd/breezy param | head -1` (header)
`go run ./cmd/breezy param | tail -1` (last param, e.g. `0x0409  indication_off_window_end  …`).

- [ ] **Step 10: Commit**

```bash
git add cmd/breezy/commands.go cmd/breezy/main.go cmd/breezy/main_test.go README.md CLAUDE.md
git commit -m "$(cat <<'EOF'
cmd/breezy: add `param` global to list the registry

Closes #3. Prints id/name/type/unit/caps/description as a wide table,
sourced from breezy.AllParams(). No daemon round-trip. README and
CLAUDE.md mention the new verb.
EOF
)"
```

---

## Out of scope (do not implement)

- `--json` output. No consumer; the registry is also published as Markdown in `docs/superpowers/specs/2026-05-03-param-map.md`.
- Filter flags (`--writable`, `--page`, `--type`). `grep` works.
- `breezy param <name>` detail view. The wide table includes the description; `breezy param | grep <name>` is the path.
- Page-grouped section headers. IDs already group naturally.
- Promoting `capsString` to `pkg/breezy`. One caller — keep it local.

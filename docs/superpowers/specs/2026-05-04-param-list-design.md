# `breezy param` — design

**Date:** 2026-05-04
**Status:** approved for implementation
**Repo:** `~/twinfresh`
**Issue:** [#3 — Params Table](https://github.com/hughobrien/breezyd/issues/3)

## Summary

Add a global `breezy param` verb that prints the static parameter registry from `pkg/breezy/params.go` as a wide ASCII table. Exists so a user reaching for `breezy <name> get <param>` or `set <param> <hex>` can discover what names exist, what type each parameter takes, and which ones are writable, without leaving the CLI.

## Motivation

The registry is already the source of truth: `get` and `set` resolve names through it (`commands.go:resolveParam`), the daemon's poller drives off it, and the param-map spec mirrors it. But there's no way to *see* it from the CLI. Users either guess names, paw through `params.go`, or open `2026-05-03-param-map.md` in a browser. A built-in lister closes that gap with a few dozen lines of code and zero new state.

## Surface

`breezy param` — global verb, no arguments. Sits next to `ls`, `discover`, `daemon-url` in dispatch. Does not contact the daemon; pure metadata read from `breezy.AllParams()`.

Output is a header row followed by one row per registered parameter, sorted by ID. Sample (truncated):

```
ID      NAME                          TYPE            UNIT    CAPS    DESCRIPTION
0x0001  power                         uint8           -       RWID    Turn the unit on/off (0=off, 1=on, 2=invert)
0x0002  speed_mode                    uint8           -       RWID    Speed preset 1-3 or 255=manual percentage mode
0x0007  timer                         uint8           -       RWID    Active special-mode (0=off, 1=night, 2=turbo)
...
0x0044  speed_manual_pct              uint8           %       RWID    Manual fan speed % (10-100, firmware floor 10)
0x004A  fan_supply_rpm                uint16          rpm     R       Live supply-fan speed
...
0x0409  indication_off_window_end     duration        hh:mm   RW      Display-off window end
```

### Column rules

- **ID** — 4-digit lowercase hex with `0x` prefix (`0x0044`). Constant width keeps the column tidy across pages 0x00, 0x01, 0x03, 0x04.
- **NAME** — `Param.Name` verbatim (snake_case, lowercase).
- **TYPE** — `Param.Type.String()` (already implemented: `uint8`, `uint16`, `int16`, `ipv4`, `ascii`, `time_of_day`, `date`, `duration`, `remaining_time`, `firmware_meta`, `alert_bitmap`, `write_only`, `raw`).
- **UNIT** — `Param.Unit` if non-empty, else `-`. Mirrors how `breezy ls` represents unknown power/mode.
- **CAPS** — concatenation of capability letters in fixed order: `R`, `W`, `I`, `D`. Read-only → `R`, the common writable case → `RW`, fully capable → `RWID`, write-only triggers → `W`.
- **DESCRIPTION** — `Param.Description` verbatim. Long lines (60+ chars) are fine; the row is wide on purpose. Users who want narrow output pipe through `cut`/`awk` or `less -S`.

### Column widths

Computed dynamically from the rendered cell contents (per-column max). Columns are separated by a two-space gutter, matching the existing `renderLs` formatter (`%-*s  %-*s  …`). The last column (description) is unpadded so trailing whitespace doesn't accumulate. Sort is stable on ID; no secondary key needed since IDs are unique by construction (the registry's `init()` panics on duplicates).

### Exit codes

- `0` always (the registry is built into the binary; there is nothing to fail).

There is no remote call, no I/O failure mode, and no user input to validate. Even on an empty registry — which would be a build-time error caught by tests — the command would print just the header.

## Implementation

Three small touches:

1. **`cmd/breezy/commands.go`** — add `cmdParam(stdout io.Writer) int`. Calls `breezy.AllParams()`, hands the slice to `renderParams`, returns 0.
2. **`cmd/breezy/render.go`** — add `renderParams(w io.Writer, params []breezy.Param)`. Computes column widths in one pass, writes the header, writes each row. Single function, no shared formatting helper introduced.
3. **`cmd/breezy/main.go`** — add `case "param": return cmdParam(stdout)` to the globals switch. Add a one-line entry to the `usage` const under **Globals**.

No new exports from `pkg/breezy`. `Param`, `Capabilities`, `ValueType.String()`, and `AllParams()` are already exported and sufficient.

### Capabilities rendering helper

Inline in `render.go`:

```go
func capsString(c breezy.Capabilities) string {
    var b strings.Builder
    if c.CanRead()  { b.WriteByte('R') }
    if c.CanWrite() { b.WriteByte('W') }
    if c.CanInc()   { b.WriteByte('I') }
    if c.CanDec()   { b.WriteByte('D') }
    return b.String()
}
```

If the helper turns out to be useful from a second call site later, promote it to `pkg/breezy` then. Not now.

## Tests

- **`cmd/breezy/main_test.go`** — black-box test through `run([]string{"param"}, …)`. Asserts exit 0, header line present, and that the row count equals `len(breezy.AllParams())`. Spot-checks the presence of `0x0001  power` and `0x0044  speed_manual_pct` in the output.
- **`cmd/breezy/render_test.go`** — direct test on `renderParams` with a small fixture slice (3-4 params covering R, RW, RWID, W). Asserts header order, column alignment by re-parsing, and that empty `Unit` renders as `-`.

No golden-file commitment. The registry grows over time; pinning the entire output would just produce noise in diffs every time a parameter is added.

## Documentation

- **`cmd/breezy/main.go` `usage` const** — append under **Globals**:

  ```
    param                 list known parameters (id, type, unit, caps)
  ```

- **`README.md`** — one mention in the CLI-surface section. The `get`/`set` paragraph should reference it: "Run `breezy param` for the registry."
- **`CLAUDE.md`** — under "## CLI surface", add `param` to the globals list. One word, no rewrite of the surrounding sentence.

## Out of scope

- `--json` output. The registry is also published as Markdown (`docs/superpowers/specs/2026-05-03-param-map.md`); a third encoding has no current consumer.
- Filter flags (`--writable`, `--page <n>`, `--type <t>`). `grep` works.
- Detail view (`breezy param <name>`). The wide table already includes the description; pulling one row is `breezy param | grep <name>`.
- Page-grouped section headers (`# Page 0`, `# Page 0x03`, …). The IDs already group naturally and a non-trivial format is harder to grep.
- Promoting `capsString` to `pkg/breezy`. One caller, stays local.
- Re-exporting `breezy.AllParams()` shape in any new way. The existing function is sufficient.

## Verification

- `just check` (lint + fast tests) passes.
- `breezy param` run against a local build prints the table without truncation or alignment glitches at 120-column width.
- The new tests in `cmd/breezy/main_test.go` and `cmd/breezy/render_test.go` pass.

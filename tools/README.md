# tools/

These two Python scripts are research artifacts from the project's
reverse-engineering phase. **They are not part of the build.** The Go
binaries (`breezyd` and `breezy`) are entirely self-contained and do not
depend on Python at all. The scripts are kept in the repo as historical
documentation and as utilities for anyone who wants to probe a new
firmware revision or a model that this project hasn't yet characterized.

## probe.py

Interactive single-parameter read/write against one device. Lets you read
any parameter by hex ID, write a value, or sweep all parameters at once.

```
python3 tools/probe.py --ip 192.168.1.X --id <16-char-device-id> --pwd <password> read 0x25
python3 tools/probe.py --ip 192.168.1.X --id <16-char-device-id> --pwd <password> read all
python3 tools/probe.py --ip 192.168.1.X --id <16-char-device-id> --pwd <password> write 0x02 2
```

The script refuses to write to credential and network parameters
(`0x7D` protocol password, `0x95` WiFi SSID, `0x96` WiFi password,
`0x9C`-`0x9F` and `0xA3` for IP/mask/gateway/DNS) so it can't accidentally
brick a unit's network configuration.

## snapshot.py

Captures the entire readable parameter state to a JSON file, and diffs
two snapshots. Used during reverse-engineering to identify which
parameter(s) changed when a single setting was toggled in the vendor app.

```
python3 tools/snapshot.py capture --ip 192.168.1.X --id <id> --pwd <pwd> baseline.json
# (operator changes one setting in the vendor app)
python3 tools/snapshot.py capture --ip 192.168.1.X --id <id> --pwd <pwd> after.json
python3 tools/snapshot.py diff baseline.json after.json
```

The diff output isolates exactly which parameters moved, which is the
core technique used to map most of the parameters in
[`docs/superpowers/specs/2026-05-03-param-map.md`](../docs/superpowers/specs/2026-05-03-param-map.md).

## Why Python and not Go?

These scripts were written during Phase 0, before the Go library existed.
Python made sense for fast iteration, REPL-style tinkering, and zero
build overhead. They've been kept in their original Python form rather
than ported because they're stable, documented, and small.

# Vendored UI assets

| File | Source | License | sha256 |
|---|---|---|---|
| htmx-2.0.4.min.js | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js | BSD-2-Clause | e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447 |
| htmx-response-targets-2.0.4.min.js | https://unpkg.com/htmx-ext-response-targets@2.0.4/dist/response-targets.js | BSD-2-Clause | 811d992edad4523f12f999688668b79cb2900e57a43c41eb0c26ae7f6669c418 |

To upgrade: bump the version in the filename, re-run the curl commands in
`docs/superpowers/plans/2026-05-06-htmx-migration.md` Task 2 Step 1, update
this table, and grep for the old version in `cmd/breezyd/`.

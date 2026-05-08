# Vendored UI assets

| File | Source | License | sha256 |
|---|---|---|---|
| datastar-1.0.1.min.js | https://github.com/starfederation/datastar v1.0.1 `bundles/datastar.js` | MIT | 54768cf34985be0229c7229f1df9469fbd32e2a0c09b4a3f1e81ad8c4d6840da |
| dashboard.js | this repo (preset-slider snap + match-speeds + implied-mode helper) | GPL-3.0-or-later | n/a (regenerated from source) |

To upgrade datastar: bump the version in the filename, re-download the
matching bundle, recompute the sha256, update this table and the
`datastarVersion` constant in `cmd/breezyd/ui_assets.go`, then grep for
the old version under `cmd/breezyd/`.

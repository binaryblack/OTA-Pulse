#!/usr/bin/env bash
# TODO-058/NEW-OTA-003: regenerate soc-ota-agent's third-party license bundle.
#
# Run this after any go.mod dependency change and commit the result --
# third_party_licenses/ is a checked-in snapshot (agent binaries are built
# and shipped separately from this checkout, so a build-time-only bundle
# wouldn't reliably reach /usr/share/doc/soc-ota-agent/ on-device without
# wiring every build path -- meta-otapulse's Yocto recipe, buildroot-otapulse,
# and any future packaging -- to regenerate it; a checked-in snapshot is
# regenerated here on demand instead, and packaging recipes just copy it).
#
# Requires: Go toolchain, `go install github.com/google/go-licenses@latest`.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! command -v go-licenses >/dev/null 2>&1; then
    echo "go-licenses not found on PATH -- install it first:" >&2
    echo "  go install github.com/google/go-licenses@latest" >&2
    exit 1
fi

OUT_DIR="third_party_licenses"
rm -rf "$OUT_DIR"
go-licenses save ./... --save_path="$OUT_DIR"

CSV_FILE="third_party_licenses.csv"
go-licenses csv ./... > "$CSV_FILE"

echo "Wrote $OUT_DIR/ and $CSV_FILE -- review the diff, then commit both."

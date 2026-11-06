#!/bin/sh
# Run the headless Xhaos backend (Wails v3 server mode) from the repo root.
# Go caches default to /tmp so they stay out of the repo and are easy to drop.
set -eu
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-/tmp/xhaos-go-cache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/xhaos-go-modcache}"
# Pure Go (SQLite included): no cgo, no system libraries.
export CGO_ENABLED=0
# The page loads the bundled React app; rebuild it first.
node scripts/build.mjs
exec "${GO:-go}" run -tags server ./cmd/xhaos "$@"

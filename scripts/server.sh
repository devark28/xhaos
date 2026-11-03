#!/bin/sh
# Run the headless Xhaos backend (Wails v3 server mode) from the repo root.
# Go caches default to /tmp so they stay out of the repo and are easy to drop.
set -eu
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-/tmp/xhaos-go-cache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/xhaos-go-modcache}"
# cgo is on only for SQLite (linked dynamically against the system libsqlite3).
# `purego` keeps Wails off its cgo GTK webview code, which server mode never uses.
export CGO_ENABLED=1
# The page loads the bundled React app; rebuild it first.
node scripts/build.mjs
exec "${GO:-go}" run -tags server,purego,libsqlite3 ./cmd/xhaos "$@"

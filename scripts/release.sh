#!/bin/sh
# Builds release archives: one static binary per platform (pure Go, no cgo),
# with the page built in, so a server needs nothing else installed.
#
#   scripts/release.sh                         linux/amd64 and linux/arm64 into release/
#   TARGETS="linux/amd64 darwin/arm64" scripts/release.sh
#   VERSION=v0.2.0 scripts/release.sh
#
# Needs Node (to build the page) and Go. macOS builds run, but Landlock is
# Linux-only: on macOS agents' commands are not sandboxed (see docs/deploy.md).
set -eu
cd "$(dirname "$0")/.."
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
OUT=${OUT:-release}
TARGETS=${TARGETS:-linux/amd64 linux/arm64}
GO=${GO:-go}

rm -rf "$OUT" && mkdir -p "$OUT"
# The page without source maps, then built into each binary.
rm -rf frontend/dist
XHAOS_RELEASE=1 node scripts/build.mjs
for target in $TARGETS; do
  os=${target%/*} arch=${target#*/}
  name="xhaos-$VERSION-$os-$arch"
  echo "building $name"
  mkdir -p "$OUT/$name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch "$GO" build -trimpath -tags server,embed \
    -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/$name/xhaos" ./cmd/xhaos
  cp README.md LICENSE install.sh "$OUT/$name/" && mkdir -p "$OUT/$name/docs" "$OUT/$name/dictation" && cp docs/deploy.md docs/guide.md "$OUT/$name/docs/"
  # The reference dictation sidecar, which install.sh sets up.
  cp scripts/dictation/stt.py "$OUT/$name/dictation/"
  tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  rm -rf "${OUT:?}/$name"
done
(cd "$OUT" && sha256sum ./*.tar.gz > SHA256SUMS)
# Back to the development page (with source maps).
node scripts/build.mjs >/dev/null
ls -la "$OUT"

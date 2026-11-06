//go:build embed

// Package frontend holds the built page for release binaries: with the
// embed build tag, index.html and dist/ (built first by scripts/build.mjs)
// are compiled into the server, so it is one file to copy and run.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed index.html all:dist
var files embed.FS

// Files is the page, or nil in builds without it (served from disk).
var Files fs.FS = files

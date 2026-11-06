//go:build !embed

package frontend

import "io/fs"

// Files is nil without the embed build tag: the page is served from disk
// (-frontend), as in development.
var Files fs.FS

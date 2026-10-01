// Package web holds the browser UI, built into the executable.
package web

import "embed"

// Files are the UI's files: index.html and the assets it loads.
//
//go:embed index.html app.js app.css icon.svg
var Files embed.FS

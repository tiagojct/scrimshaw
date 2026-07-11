// Package static holds the interface's stylesheet and script, embedded into
// the binary and served as same-origin assets so the Content-Security-Policy
// needs no inline exceptions.
package static

import "embed"

//go:embed app.css app.js
var Files embed.FS

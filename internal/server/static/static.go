// Package static holds the interface's stylesheet and script, embedded into
// the binary and served as same-origin assets so the Content-Security-Policy
// needs no inline exceptions.
package static

import "embed"

// The accent color's source of truth is accent.json; `go generate` rewrites
// the marked block in app.css from it (see gentokens/main.go). This never
// runs at build or request time, so app.css stays a plain static file.
//go:generate go run ./gentokens

//go:embed app.css app.js
var Files embed.FS

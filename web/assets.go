package webassets

import "embed"

// Files contains the browser assets served by the application.
//
//go:embed manifest.webmanifest service-worker.js
var Files embed.FS

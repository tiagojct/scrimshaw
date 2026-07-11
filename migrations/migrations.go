package migrations

import "embed"

// Files contains the ordered SQL migrations used by the datastore.
//
//go:embed *.sql
var Files embed.FS

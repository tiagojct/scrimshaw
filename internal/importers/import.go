package importers

import (
	"context"
	"fmt"
	"io"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// Import selects a supported migration format.
func Import(ctx context.Context, format string, input io.Reader, destination *store.Store) (int, error) {
	switch format {
	case "netscape", "pocket":
		return NetscapeBookmarks(ctx, input, destination)
	case "instapaper":
		return Instapaper(ctx, input, destination)
	case "linkding":
		return Linkding(ctx, input, destination)
	case "readeck":
		return Readeck(ctx, input, destination)
	case "opml":
		return OPML(ctx, input, destination)
	default:
		return 0, fmt.Errorf("unsupported import format")
	}
}

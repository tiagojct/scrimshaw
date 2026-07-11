package importers

import (
	"context"
	"encoding/json"
	"io"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// Readeck imports bookmark JSON from Readeck API and backup exports. Both a
// top-level list and an object containing bookmarks are accepted.
func Readeck(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	var document json.RawMessage
	if err := json.NewDecoder(io.LimitReader(input, 10<<20)).Decode(&document); err != nil {
		return 0, err
	}
	var bookmarks []readeckBookmark
	if err := json.Unmarshal(document, &bookmarks); err != nil {
		var envelope struct {
			Bookmarks []readeckBookmark `json:"bookmarks"`
			Results   []readeckBookmark `json:"results"`
		}
		if envelopeErr := json.Unmarshal(document, &envelope); envelopeErr != nil {
			return 0, err
		}
		bookmarks = envelope.Bookmarks
		if len(bookmarks) == 0 {
			bookmarks = envelope.Results
		}
	}
	var imported int
	for _, bookmark := range bookmarks {
		id, err := destination.InsertManualItem(ctx, bookmark.URL, bookmark.Title, "", "", bookmark.Content, bookmark.Labels, true)
		if err != nil {
			continue
		}
		if bookmark.Archived {
			if err := destination.SetArchived(ctx, id, true); err != nil {
				return imported, err
			}
		}
		imported++
	}
	return imported, nil
}

type readeckBookmark struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Labels   []string `json:"labels"`
	Content  string   `json:"content"`
	Archived bool     `json:"archived"`
}

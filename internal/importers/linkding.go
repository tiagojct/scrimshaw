package importers

import (
	"context"
	"encoding/json"
	"io"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// Linkding imports bookmark objects returned by linkding's documented REST API.
// It accepts either a top-level array or the paginated {"results": [...]} response.
func Linkding(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	var document json.RawMessage
	if err := json.NewDecoder(io.LimitReader(input, 10<<20)).Decode(&document); err != nil {
		return 0, err
	}
	var bookmarks []linkdingBookmark
	if err := json.Unmarshal(document, &bookmarks); err != nil {
		var page struct {
			Results []linkdingBookmark `json:"results"`
		}
		if pageErr := json.Unmarshal(document, &page); pageErr != nil {
			return 0, err
		}
		bookmarks = page.Results
	}
	var imported int
	for _, bookmark := range bookmarks {
		id, err := destination.InsertManualItem(ctx, bookmark.URL, bookmark.Title, "", "", bookmark.Description+"\n"+bookmark.Notes, bookmark.TagNames)
		if err != nil {
			continue
		}
		if bookmark.Unread {
			if err := destination.SetReadState(ctx, id, "unread"); err != nil {
				return imported, err
			}
		} else if err := destination.SetReadState(ctx, id, "read"); err != nil {
			return imported, err
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

type linkdingBookmark struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Notes       string   `json:"notes"`
	TagNames    []string `json:"tag_names"`
	Unread      bool     `json:"unread"`
	Archived    bool     `json:"is_archived"`
}

package importers

import (
	"context"
	"strings"
	"testing"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestNetscapeBookmarksImportsLinksWithFolderTag(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	html := `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><DT><H3>Reading</H3><DL><DT><A HREF="https://example.com/article">Example</A></DL></DL>`
	count, err := NetscapeBookmarks(context.Background(), strings.NewReader(html), db)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("imported %d bookmarks, want 1", count)
	}
	items, total, err := db.ListPage(context.Background(), store.ListOptions{Tag: "Reading"})
	if err != nil || total != 1 || items[0].Title != "Example" {
		t.Fatalf("tagged import mismatch: items=%v total=%d err=%v", items, total, err)
	}
}

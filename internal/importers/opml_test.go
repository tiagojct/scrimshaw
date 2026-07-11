package importers

import (
	"context"
	"strings"
	"testing"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestOPMLImportsFeeds(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count, err := OPML(context.Background(), strings.NewReader(`<opml><body><outline text="Feeds"><outline xmlUrl="https://example.com/feed"/></outline></body></opml>`), db)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	feeds, err := db.AllFeeds(context.Background())
	if err != nil || len(feeds) != 1 {
		t.Fatalf("feeds=%v err=%v", feeds, err)
	}
}

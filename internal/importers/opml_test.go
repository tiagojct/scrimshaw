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

func TestParseOPMLNestedWithTitles(t *testing.T) {
	feeds, err := ParseOPML(strings.NewReader(`<opml><body>
		<outline text="Kottke" title="Kottke" xmlUrl="https://kottke.org/feed"/>
		<outline text="Tech"><outline text="Ars" xmlUrl="https://arstechnica.com/feed"/></outline>
	</body></opml>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 2 {
		t.Fatalf("want 2 feeds, got %d: %+v", len(feeds), feeds)
	}
	if feeds[0].Title != "Kottke" || feeds[0].URL != "https://kottke.org/feed" {
		t.Fatalf("first feed: %+v", feeds[0])
	}
	// Falls back to text when title is absent, and recurses into folders.
	if feeds[1].Title != "Ars" || feeds[1].URL != "https://arstechnica.com/feed" {
		t.Fatalf("nested feed: %+v", feeds[1])
	}
}

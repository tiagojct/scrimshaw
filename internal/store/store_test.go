package store

import (
	"context"
	"testing"
	"time"
)

func TestMigrationsCreateFTSAndDeduplicateItems(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	inserted, err := s.InsertFeedItem(context.Background(), 1, "https://example.com/article#fragment", "A title", "Author", "body text", time.Time{})
	if err == nil || inserted {
		t.Fatal("foreign-key failure expected without a feed")
	}
	feedID, err := s.AddFeed(context.Background(), "https://example.com/feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err = s.InsertFeedItem(context.Background(), feedID, "https://example.com/article#fragment", "A title", "Author", "body text", time.Time{})
	if err != nil || !inserted {
		t.Fatalf("first insert: inserted=%v err=%v", inserted, err)
	}
	inserted, err = s.InsertFeedItem(context.Background(), feedID, "https://example.com/article", "Changed title", "Author", "body text", time.Time{})
	if err != nil || inserted {
		t.Fatalf("duplicate insert: inserted=%v err=%v", inserted, err)
	}
	var matches int
	if err := s.DB.QueryRow(`SELECT count(*) FROM items_fts WHERE items_fts MATCH 'body'`).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("FTS matches = %d, want 1", matches)
	}
	inserted, err = s.InsertFeedItem(context.Background(), feedID, "https://another.example/article", "A title", "Author", "body text", time.Time{})
	if err != nil || inserted {
		t.Fatalf("content duplicate insert: inserted=%v err=%v", inserted, err)
	}
	if err := s.SetSnapshot(context.Background(), 1, "/snapshots/1.html", "offline archive body"); err != nil {
		t.Fatal(err)
	}
	var snapshotMatches int
	if err := s.DB.QueryRow(`SELECT count(*) FROM items_fts WHERE items_fts MATCH 'offline'`).Scan(&snapshotMatches); err != nil {
		t.Fatal(err)
	}
	if snapshotMatches != 1 {
		t.Fatalf("snapshot FTS matches = %d, want 1", snapshotMatches)
	}
}

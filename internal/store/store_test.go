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

func TestBookmarksReadLaterDatesAndLinkChecks(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	bookmarkID, err := s.InsertManualItem(ctx, "https://link.example/a", "A link", "", "", "", nil, false)
	if err != nil {
		t.Fatalf("insert bookmark: %v", err)
	}
	articleID, err := s.InsertManualItem(ctx, "https://read.example/b", "An article", "", "", "<p>body</p>", nil, true)
	if err != nil {
		t.Fatalf("insert article: %v", err)
	}

	// A link-only save is a bookmark; the bookmarks view is flag-based.
	if item, _ := s.Item(ctx, bookmarkID); !item.Bookmarked || item.ReadLater {
		t.Fatalf("bookmark flags wrong: %+v", item)
	}
	bookmarks, _, err := s.ListPage(ctx, ListOptions{Bookmarked: "1"})
	if err != nil || len(bookmarks) != 1 || bookmarks[0].ID != bookmarkID {
		t.Fatalf("bookmarks view = %+v err=%v", bookmarks, err)
	}
	later, _, err := s.ListPage(ctx, ListOptions{ReadLater: "1"})
	if err != nil || len(later) != 1 || later[0].ID != articleID {
		t.Fatalf("read-later view = %+v err=%v", later, err)
	}

	// Reading files an item away: read stamps read_at and archives; unread reverses both.
	if err := s.SetReadState(ctx, articleID, "read"); err != nil {
		t.Fatal(err)
	}
	item, err := s.Item(ctx, articleID)
	if err != nil || !item.ReadAt.Valid || !item.Archived {
		t.Fatalf("read should stamp read_at and archive: item=%+v err=%v", item, err)
	}
	if err := s.SetReadState(ctx, articleID, "unread"); err != nil {
		t.Fatal(err)
	}
	if item, _ = s.Item(ctx, articleID); item.ReadAt.Valid || item.Archived {
		t.Fatal("unread should clear read_at and unarchive")
	}

	// A never-checked link is due; recording a status clears it from the queue.
	due, err := s.LinksToCheck(ctx, time.Now(), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("links to check = %d err=%v", len(due), err)
	}
	if err := s.SetLinkStatus(ctx, bookmarkID, 404); err != nil {
		t.Fatal(err)
	}
	if item, _ = s.Item(ctx, bookmarkID); item.LinkStatus != 404 || !item.LinkCheckedAt.Valid {
		t.Fatalf("link status not recorded: %+v", item)
	}
	due, err = s.LinksToCheck(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(due) != 1 || due[0].ID != articleID {
		t.Fatalf("after check, due = %+v err=%v", due, err)
	}

	// A note-only annotation (empty quote) is allowed; empty quote and note is not.
	if err := s.AddHighlight(ctx, articleID, "", "a standalone note", 0); err != nil {
		t.Fatalf("note-only highlight rejected: %v", err)
	}
	if err := s.AddHighlight(ctx, articleID, "  ", "  ", 0); err == nil {
		t.Fatal("expected error for empty quote and note")
	}

	// Shared items feed the website linklog/read-articles split.
	if err := s.SetShared(ctx, bookmarkID, true); err != nil {
		t.Fatal(err)
	}
	shared, _, err := s.ListPage(ctx, ListOptions{Shared: "1", ReadLater: "0"})
	if err != nil || len(shared) != 1 || shared[0].ID != bookmarkID {
		t.Fatalf("shared linklog = %+v err=%v", shared, err)
	}
}

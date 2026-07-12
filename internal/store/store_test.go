package store

import (
	"context"
	"errors"
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

	// Only bookmarks are link-checked. The bookmark is due; the read-later
	// article is not. Recording a status clears the bookmark from the queue.
	due, err := s.LinksToCheck(ctx, time.Now(), 10)
	if err != nil || len(due) != 1 || due[0].ID != bookmarkID {
		t.Fatalf("links to check = %+v err=%v", due, err)
	}
	if err := s.SetLinkStatus(ctx, bookmarkID, 404); err != nil {
		t.Fatal(err)
	}
	if item, _ = s.Item(ctx, bookmarkID); item.LinkStatus != 404 || !item.LinkCheckedAt.Valid {
		t.Fatalf("link status not recorded: %+v", item)
	}
	due, err = s.LinksToCheck(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(due) != 0 {
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

func TestBookmarkDedupAndBulkRead(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Two different URLs with the same title must both save: a link-only
	// bookmark's content hash must not collide on title alone.
	a, err := s.InsertManualItem(ctx, "https://one.example/x", "Untitled", "", "", "", nil, false)
	if err != nil {
		t.Fatalf("first bookmark: %v", err)
	}
	b, err := s.InsertManualItem(ctx, "https://two.example/y", "Untitled", "", "", "", nil, false)
	if err != nil {
		t.Fatalf("same-title bookmark should still save: %v", err)
	}
	if a == b {
		t.Fatal("distinct URLs returned the same id")
	}
	// The same URL is a real duplicate and must be rejected (not return a stale id).
	if _, err := s.InsertManualItem(ctx, "https://one.example/x", "Untitled", "", "", "", nil, false); err == nil {
		t.Fatal("duplicate URL should be rejected")
	}

	// Bulk mark-read mirrors single mark-read: it stamps read_at and archives.
	if err := s.BulkUpdate(ctx, []int64{a}, "read"); err != nil {
		t.Fatal(err)
	}
	item, _ := s.Item(ctx, a)
	if item.ReadState != "read" || !item.Archived || !item.ReadAt.Valid {
		t.Fatalf("bulk read should archive and stamp read_at: %+v", item)
	}

	// The "All" view: no filters plus IncludeArchived must not build an empty
	// WHERE clause, and must return archived items too.
	all, total, err := s.ListPage(ctx, ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("all view errored: %v", err)
	}
	if total != 2 || len(all) != 2 {
		t.Fatalf("all view should return both items incl archived: total=%d len=%d", total, len(all))
	}
}

func TestTagsAndDelete(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.InsertManualItem(ctx, "https://ex/a", "An item", "", "", "<p>body</p>", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// SetItemTags replaces and de-duplicates (case-insensitively).
	if err := s.SetItemTags(ctx, id, []string{"News", "tech", "news", "  "}); err != nil {
		t.Fatal(err)
	}
	tags, err := s.ItemTags(ctx, id)
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags after set = %v err=%v", tags, err)
	}
	if err := s.SetItemTags(ctx, id, []string{"solo"}); err != nil {
		t.Fatal(err)
	}
	if tags, _ = s.ItemTags(ctx, id); len(tags) != 1 || tags[0] != "solo" {
		t.Fatalf("tags should be replaced: %v", tags)
	}

	// Delete cascades to highlights and tags.
	if err := s.AddHighlight(ctx, id, "quote", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Item(ctx, id); err == nil {
		t.Fatal("item should be gone")
	}
	hs, _ := s.HighlightsForItem(ctx, id)
	tags, _ = s.ItemTags(ctx, id)
	if len(hs) != 0 || len(tags) != 0 {
		t.Fatalf("delete should cascade: highlights=%d tags=%d", len(hs), len(tags))
	}
}

func TestFeedSettingsAndReenable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fid, err := s.AddFeed(ctx, "https://feed.example/x", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Auto-disable it, then confirm it drops out of the due list.
	if err := s.RecordFeedFailure(ctx, Feed{ID: fid}, errors.New("boom"), 1); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Feed(ctx, fid); !f.Disabled {
		t.Fatal("feed should be disabled after failure")
	}

	// Settings update.
	if err := s.SetFeedRefresh(ctx, fid, 6*time.Hour, true, false); err != nil {
		t.Fatal(err)
	}
	f, err := s.Feed(ctx, fid)
	if err != nil || f.RefreshInterval != 6*time.Hour || !f.FetchFullContent || f.AutoSnapshot {
		t.Fatalf("feed settings not applied: %+v err=%v", f, err)
	}

	// Re-enable clears the disabled state and last error.
	if err := s.EnableFeed(ctx, fid); err != nil {
		t.Fatal(err)
	}
	if f, _ = s.Feed(ctx, fid); f.Disabled || f.LastError != "" {
		t.Fatalf("re-enable should clear disabled and error: %+v", f)
	}
}

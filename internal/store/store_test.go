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
	// Two distinct URLs sharing a title but with no body must both insert: a
	// bodiless item's content hash must not collide on title alone.
	if ins, err := s.InsertFeedItem(context.Background(), feedID, "https://a.example/1", "New post", "", "", time.Time{}); err != nil || !ins {
		t.Fatalf("first bodiless item: inserted=%v err=%v", ins, err)
	}
	if ins, err := s.InsertFeedItem(context.Background(), feedID, "https://a.example/2", "New post", "", "", time.Time{}); err != nil || !ins {
		t.Fatalf("second same-title bodiless item should still insert: inserted=%v err=%v", ins, err)
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

func TestInsertManualItemReturnsErrItemExistsOnDuplicateURL(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	feedID, err := s.AddFeed(ctx, "https://ex/feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertFeedItem(ctx, feedID, "https://ex/dup", "Feed item", "", "body", time.Time{}); err != nil {
		t.Fatal(err)
	}

	// A caller (e.g. saving a feed item's URL again as a bookmark) must be able
	// to distinguish "already exists" from other failures via errors.Is, so the
	// existing item can be looked up and merged into instead of erroring.
	_, err = s.InsertManualItem(ctx, "https://ex/dup", "Bookmark title", "", "", "", nil, false)
	if !errors.Is(err, ErrItemExists) {
		t.Fatalf("InsertManualItem on a duplicate canonical_url = %v, want ErrItemExists", err)
	}
}

func TestRenameTag(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.InsertManualItem(ctx, "https://ex/a", "An item", "", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemTags(ctx, id, []string{"News", "tech"}); err != nil {
		t.Fatal(err)
	}

	if err := s.RenameTag(ctx, "News", "Current Events"); err != nil {
		t.Fatal(err)
	}
	tags, err := s.ItemTags(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(tags, "Current Events") || slicesContain(tags, "News") {
		t.Fatalf("tags after rename = %v", tags)
	}

	if err := s.RenameTag(ctx, "Current Events", "tech"); err == nil {
		t.Fatal("renaming to an already-used name should fail (use merge instead)")
	}
	if err := s.RenameTag(ctx, "no-such-tag", "whatever"); err == nil {
		t.Fatal("renaming a nonexistent tag should fail")
	}
}

func TestMergeTag(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id1, err := s.InsertManualItem(ctx, "https://ex/a", "Item A", "", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.InsertManualItem(ctx, "https://ex/b", "Item B", "", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// id1 carries both tags already, exercising the PK-collision path the
	// INSERT OR IGNORE has to tolerate; id2 carries only the merge source.
	if err := s.SetItemTags(ctx, id1, []string{"golang", "go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemTags(ctx, id2, []string{"golang"}); err != nil {
		t.Fatal(err)
	}

	if err := s.MergeTag(ctx, "golang", "go"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{id1, id2} {
		tags, err := s.ItemTags(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) != 1 || tags[0] != "go" {
			t.Fatalf("item %d tags after merge = %v, want just [go]", id, tags)
		}
	}
	counts, err := s.AllTagCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Name != "go" || counts[0].Count != 2 {
		t.Fatalf("tag counts after merge = %v, want just go:2", counts)
	}

	if err := s.MergeTag(ctx, "go", "go"); err == nil {
		t.Fatal("merging a tag into itself should fail")
	}
	if err := s.MergeTag(ctx, "no-such-tag", "go"); err == nil {
		t.Fatal("merging a nonexistent source tag should fail")
	}
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
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

func TestUnreadTagCountsScopedToCollection(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	feedID, err := s.AddFeed(ctx, "https://ex/feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertFeedItem(ctx, feedID, "https://ex/feed-item", "Feed item", "", "body", time.Time{}); err != nil {
		t.Fatal(err)
	}
	feedItemID, err := s.ItemIDByURL(ctx, "https://ex/feed-item")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemTags(ctx, feedItemID, []string{"news"}); err != nil {
		t.Fatal(err)
	}

	bookmarkID, err := s.InsertManualItem(ctx, "https://ex/bookmark", "A bookmark", "", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemTags(ctx, bookmarkID, []string{"reading"}); err != nil {
		t.Fatal(err)
	}

	// The Feeds view's tag bar must show only tags on feed items, not the
	// unrelated bookmark's tag.
	counts, err := s.UnreadTagCounts(ctx, ListOptions{Source: "feed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Name != "news" {
		t.Fatalf("feed-scoped tag counts = %v, want just [news]", counts)
	}

	// The Bookmarks view must show only the bookmark's tag, not the feed item's.
	counts, err = s.UnreadTagCounts(ctx, ListOptions{Bookmarked: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Name != "reading" {
		t.Fatalf("bookmark-scoped tag counts = %v, want just [reading]", counts)
	}
}

func TestListPageSinceUntilFiltersByDate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	feedID, err := s.AddFeed(ctx, "https://ex/feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	yesterday := today.Add(-24 * time.Hour)
	if _, err := s.InsertFeedItem(ctx, feedID, "https://ex/today", "Today item", "", "body", today); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertFeedItem(ctx, feedID, "https://ex/yesterday", "Yesterday item", "", "body", yesterday); err != nil {
		t.Fatal(err)
	}

	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	items, total, err := s.ListPage(ctx, ListOptions{
		Since: start.Format(time.RFC3339), Until: start.Add(24 * time.Hour).Format(time.RFC3339),
		IncludeArchived: true, PerPage: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Title != "Today item" {
		t.Fatalf("Since/Until filter = %d items %v, want just [Today item]", total, items)
	}
}

func TestOptimizeFTS(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.InsertManualItem(ctx, "https://ex/optimize", "Optimize me", "", "", "<p>some searchable body text</p>", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := s.OptimizeFTS(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := s.Search(ctx, "searchable")
	if err != nil || len(items) != 1 {
		t.Fatalf("search after optimize = %v items, err=%v", items, err)
	}
}

func TestSetUserPasswordHashAndDeleteAllSessions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateUser(ctx, "original-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "sess1", "csrf1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.SetUserPasswordHash(ctx, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if hash, err := s.UserPasswordHash(ctx); err != nil || hash != "new-hash" {
		t.Fatalf("password hash after update = %q, err=%v", hash, err)
	}

	if err := s.DeleteAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionCSRF(ctx, "sess1"); err == nil {
		t.Fatal("session should be gone after DeleteAllSessions")
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

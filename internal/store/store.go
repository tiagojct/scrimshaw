package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tiagojct/scrimshaw/migrations"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

type Item struct {
	ID                                                                           int64
	URL, CanonicalURL, Title, Author, SiteName, ExtractedText, ReadState, Source string
	SnapshotPath                                                                 sql.NullString
	FeedID                                                                       sql.NullInt64
	PublishedAt, ReadAt, LinkCheckedAt                                           sql.NullString
	AddedAt                                                                      time.Time
	Starred, Archived, ReadLater, Bookmarked, Shared                             bool
	LinkStatus                                                                   int
}

// itemColumns is the shared projection for reading items; keep the scan in
// scanItem aligned with this list.
const itemColumns = `i.id, i.url, i.canonical_url, i.title, i.author, i.site_name, i.extracted_text, i.read_state, i.source, i.snapshot_path, i.feed_id, i.published_at, i.added_at, i.read_at, i.starred, i.archived, i.read_later, i.bookmarked, i.shared, i.link_status, i.link_checked_at`

func scanItem(rows interface{ Scan(...any) error }) (Item, error) {
	var item Item
	var added string
	err := rows.Scan(&item.ID, &item.URL, &item.CanonicalURL, &item.Title, &item.Author, &item.SiteName,
		&item.ExtractedText, &item.ReadState, &item.Source, &item.SnapshotPath, &item.FeedID, &item.PublishedAt,
		&added, &item.ReadAt, &item.Starred, &item.Archived, &item.ReadLater, &item.Bookmarked, &item.Shared, &item.LinkStatus, &item.LinkCheckedAt)
	item.AddedAt, _ = time.Parse(time.RFC3339, added)
	return item, err
}

type Feed struct {
	ID                                        int64
	URL, Title, ETag, LastModified, LastError string
	RefreshInterval                           time.Duration
	FetchFullContent, AutoSnapshot, Disabled  bool
}

type ListOptions struct {
	Tag, State, ItemType, Sort, Source string
	ReadLater, Bookmarked, Shared      string // "1", "0", or "" for no filter
	IncludeArchived                    bool   // keep archived items in the default view
	Page, PerPage                      int
}

type Count struct {
	Name  string
	Count int
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		version := filepath.Base(name)
		var applied int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", version, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("URL must be absolute")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("URL must use HTTP or HTTPS")
	}
	u.Fragment, u.User = "", nil
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	return u.String(), nil
}

func contentHash(title, text string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

func (s *Store) AddFeed(ctx context.Context, rawURL string, refresh time.Duration, tags []string) (int64, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
	if refresh < time.Minute {
		refresh = time.Hour
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO feeds(url, refresh_interval_seconds) VALUES (?, ?)`, canonical, int(refresh.Seconds()))
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	id, _ := result.LastInsertId()
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO tags(name) VALUES (?) ON CONFLICT(name) DO NOTHING", name); err != nil {
			tx.Rollback()
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO feed_tags(feed_id, tag_id) SELECT ?, id FROM tags WHERE name = ? COLLATE NOCASE`, id, name); err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) DueFeeds(ctx context.Context, now time.Time) ([]Feed, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, url, title, refresh_interval_seconds, COALESCE(etag,''), COALESCE(last_modified,''), COALESCE(last_error,''), fetch_full_content, auto_snapshot, disabled
		FROM feeds WHERE disabled = 0 AND (last_fetched IS NULL OR datetime(last_fetched, '+' || refresh_interval_seconds || ' seconds') <= datetime(?))`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []Feed
	for rows.Next() {
		var f Feed
		var seconds int
		var fetchFullContent, autoSnapshot, disabled int
		if err := rows.Scan(&f.ID, &f.URL, &f.Title, &seconds, &f.ETag, &f.LastModified, &f.LastError, &fetchFullContent, &autoSnapshot, &disabled); err != nil {
			return nil, err
		}
		f.RefreshInterval, f.FetchFullContent, f.AutoSnapshot, f.Disabled = time.Duration(seconds)*time.Second, fetchFullContent != 0, autoSnapshot != 0, disabled != 0
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

func (s *Store) RecordFeedSuccess(ctx context.Context, f Feed, title, etag, modified string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE feeds SET title=?, etag=?, last_modified=?, last_fetched=?, consecutive_failures=0, last_error=NULL WHERE id=?`,
		title, etag, modified, time.Now().UTC().Format(time.RFC3339), f.ID)
	return err
}

func (s *Store) RecordFeedFailure(ctx context.Context, f Feed, cause error, disableAfter int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE feeds SET consecutive_failures=consecutive_failures+1, last_error=?, last_fetched=?,
		disabled=CASE WHEN consecutive_failures + 1 >= ? THEN 1 ELSE 0 END WHERE id=?`,
		cause.Error(), time.Now().UTC().Format(time.RFC3339), disableAfter, f.ID)
	return err
}

func (s *Store) InsertFeedItem(ctx context.Context, feedID int64, rawURL, title, author, text string, published time.Time) (bool, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO items(url, canonical_url, content_hash, title, author, source, feed_id, published_at, added_at, extracted_text)
		VALUES (?, ?, ?, ?, ?, 'feed', ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		rawURL, canonical, contentHash(title, text), title, author, feedID, nullableTime(published), time.Now().UTC().Format(time.RFC3339), text)
	if err != nil {
		tx.Rollback()
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		tx.Rollback()
		return n > 0, err
	}
	itemID, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO item_tags(item_id, tag_id)
		SELECT ?, tag_id FROM feed_tags WHERE feed_id = ?`, itemID, feedID); err != nil {
		tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Store) ListItems(ctx context.Context) ([]Item, error) {
	items, _, err := s.ListPage(ctx, ListOptions{PerPage: 100})
	return items, err
}

func (s *Store) AllItems(ctx context.Context) ([]Item, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+itemColumns+` FROM items i ORDER BY i.added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) AllFeeds(ctx context.Context) ([]Feed, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, url, title, refresh_interval_seconds, COALESCE(etag,''), COALESCE(last_modified,''), COALESCE(last_error,''), fetch_full_content, auto_snapshot, disabled FROM feeds ORDER BY title, url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var feeds []Feed
	for rows.Next() {
		var f Feed
		var seconds, full, snapshot, disabled int
		if err := rows.Scan(&f.ID, &f.URL, &f.Title, &seconds, &f.ETag, &f.LastModified, &f.LastError, &full, &snapshot, &disabled); err != nil {
			return nil, err
		}
		f.RefreshInterval, f.FetchFullContent, f.AutoSnapshot, f.Disabled = time.Duration(seconds)*time.Second, full != 0, snapshot != 0, disabled != 0
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

func (s *Store) ListPage(ctx context.Context, options ListOptions) ([]Item, int, error) {
	if options.PerPage < 1 || options.PerPage > 100 {
		options.PerPage = 50
	}
	if options.Page < 1 {
		options.Page = 1
	}
	var where []string
	args := []any{}
	switch options.State {
	case "archived":
		where = append(where, "i.archived=1")
	case "starred":
		where = append(where, "i.starred=1")
	case "unread", "read":
		where = append(where, "i.archived=0", "i.read_state=?")
		args = append(args, options.State)
	default:
		if !options.IncludeArchived {
			where = append(where, "i.archived=0")
		}
	}
	if options.Source == "feed" || options.Source == "manual" {
		where = append(where, "i.source=?")
		args = append(args, options.Source)
	}
	if options.ReadLater == "1" || options.ReadLater == "0" {
		where = append(where, "i.read_later="+options.ReadLater)
	}
	if options.Bookmarked == "1" || options.Bookmarked == "0" {
		where = append(where, "i.bookmarked="+options.Bookmarked)
	}
	if options.Shared == "1" {
		where = append(where, "i.shared=1")
	}
	if options.Tag != "" {
		where = append(where, `EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id=it.tag_id WHERE it.item_id=i.id AND t.name=? COLLATE NOCASE)`)
		args = append(args, options.Tag)
	}
	if options.ItemType != "" {
		where = append(where, "i.item_type=?")
		args = append(args, options.ItemType)
	}
	condition := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM items i WHERE "+condition, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "COALESCE(i.published_at, i.added_at) DESC"
	switch options.Sort {
	case "oldest":
		order = "COALESCE(i.published_at, i.added_at) ASC"
	case "unread":
		order = "CASE i.read_state WHEN 'unread' THEN 0 ELSE 1 END, COALESCE(i.published_at, i.added_at) DESC"
	}
	pageArgs := append(append([]any{}, args...), options.PerPage, (options.Page-1)*options.PerPage)
	rows, err := s.DB.QueryContext(ctx, `SELECT `+itemColumns+`
		FROM items i WHERE `+condition+` ORDER BY `+order+` LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) UnreadTagCounts(ctx context.Context) ([]Count, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.name, COUNT(*) FROM tags t JOIN item_tags it ON it.tag_id=t.id JOIN items i ON i.id=it.item_id WHERE i.archived=0 AND i.read_state='unread' GROUP BY t.id ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var counts []Count
	for rows.Next() {
		var count Count
		if err := rows.Scan(&count.Name, &count.Count); err != nil {
			return nil, err
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

func (s *Store) Item(ctx context.Context, id int64) (Item, error) {
	return scanItem(s.DB.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items i WHERE i.id=?`, id))
}

// ItemTags returns an item's tag names, sorted.
func (s *Store) ItemTags(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.name FROM tags t JOIN item_tags it ON it.tag_id=t.id WHERE it.item_id=? ORDER BY t.name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}

func (s *Store) SetReadState(ctx context.Context, id int64, state string) error {
	if state != "read" && state != "unread" {
		return errors.New("invalid read state")
	}
	// Reading an item files it away: read stamps read_at and archives; unread
	// reverses both, bringing the item back to its active list.
	if state == "read" {
		_, err := s.DB.ExecContext(ctx, "UPDATE items SET read_state='read', read_at=COALESCE(read_at, ?), archived=1 WHERE id=?", time.Now().UTC().Format(time.RFC3339), id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET read_state='unread', read_at=NULL, archived=0 WHERE id=?", id)
	return err
}

func (s *Store) SetArchived(ctx context.Context, id int64, archived bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET archived=? WHERE id=?", archived, id)
	return err
}

func (s *Store) SetStarred(ctx context.Context, id int64, starred bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET starred=? WHERE id=?", starred, id)
	return err
}

func (s *Store) SetShared(ctx context.Context, id int64, shared bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET shared=? WHERE id=?", shared, id)
	return err
}

// LinksToCheck returns manually stored links whose reachability has not been
// verified since `before`, oldest check first, for the dead-link checker.
func (s *Store) LinksToCheck(ctx context.Context, before time.Time, limit int) ([]Item, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+itemColumns+` FROM items i
		WHERE (i.bookmarked=1 OR i.source='manual') AND (i.link_checked_at IS NULL OR i.link_checked_at < ?)
		ORDER BY i.link_checked_at IS NOT NULL, i.link_checked_at LIMIT ?`,
		before.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SetLinkStatus records the outcome of a dead-link check. status is an HTTP
// status code, or a negative value for a transport error.
func (s *Store) SetLinkStatus(ctx context.Context, id int64, status int) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET link_status=?, link_checked_at=? WHERE id=?",
		status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// Stats summarizes the datastore for the dashboard.
type Stats struct {
	UnreadFeeds, ReadLaterUnread, Bookmarks, BrokenLinks, Highlights, Starred, Shared int
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	// Bookmarks and Starred are permanent collections, counted regardless of
	// archive state so the tiles match their views.
	row := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM items WHERE source='feed' AND archived=0 AND read_state='unread'),
		(SELECT COUNT(*) FROM items WHERE read_later=1 AND archived=0 AND read_state='unread'),
		(SELECT COUNT(*) FROM items WHERE bookmarked=1),
		(SELECT COUNT(*) FROM items WHERE bookmarked=1 AND (link_status>=400 OR link_status<0)),
		(SELECT COUNT(*) FROM highlights),
		(SELECT COUNT(*) FROM items WHERE starred=1),
		(SELECT COUNT(*) FROM items WHERE shared=1)`)
	err := row.Scan(&st.UnreadFeeds, &st.ReadLaterUnread, &st.Bookmarks, &st.BrokenLinks, &st.Highlights, &st.Starred, &st.Shared)
	return st, err
}

func (s *Store) SetReadLater(ctx context.Context, id int64, readLater bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET read_later=? WHERE id=?", readLater, id)
	return err
}

func (s *Store) SetBookmarked(ctx context.Context, id int64, bookmarked bool) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET bookmarked=? WHERE id=?", bookmarked, id)
	return err
}

// SetContent fills in a bookmark's extracted article after the fact, e.g. when
// a stored link is promoted to Read Later.
func (s *Store) SetContent(ctx context.Context, id int64, title, author, siteName, content string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE items SET extracted_text=?,
		title=CASE WHEN ?<>'' THEN ? ELSE title END,
		author=CASE WHEN ?<>'' THEN ? ELSE author END,
		site_name=CASE WHEN ?<>'' THEN ? ELSE site_name END WHERE id=?`,
		content, title, title, author, author, siteName, siteName, id)
	return err
}

type Highlight struct {
	ID, ItemID, Position int64
	Quote, Note          string
	CreatedAt            time.Time
}

func (s *Store) AddHighlight(ctx context.Context, itemID int64, quote, note string, position int64) error {
	if strings.TrimSpace(quote) == "" {
		return errors.New("highlight quote cannot be empty")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO highlights(item_id, quote, note, position, created_at) VALUES (?, ?, ?, ?, ?)`,
		itemID, quote, note, position, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ListHighlights(ctx context.Context) ([]Highlight, error) {
	return s.scanHighlights(ctx, `SELECT id, item_id, quote, note, position, created_at FROM highlights ORDER BY created_at DESC`)
}

// HighlightsForItem returns the highlights of a single item, oldest first.
func (s *Store) HighlightsForItem(ctx context.Context, itemID int64) ([]Highlight, error) {
	return s.scanHighlights(ctx, `SELECT id, item_id, quote, note, position, created_at FROM highlights WHERE item_id = ? ORDER BY created_at`, itemID)
}

func (s *Store) scanHighlights(ctx context.Context, query string, args ...any) ([]Highlight, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var highlights []Highlight
	for rows.Next() {
		var highlight Highlight
		var created string
		if err := rows.Scan(&highlight.ID, &highlight.ItemID, &highlight.Quote, &highlight.Note, &highlight.Position, &created); err != nil {
			return nil, err
		}
		highlight.CreatedAt, _ = time.Parse(time.RFC3339, created)
		highlights = append(highlights, highlight)
	}
	return highlights, rows.Err()
}

func (s *Store) BulkUpdate(ctx context.Context, ids []int64, action string) error {
	if len(ids) == 0 {
		return errors.New("no items selected")
	}
	column, value := "", any(nil)
	switch action {
	case "read":
		column, value = "read_state", "read"
	case "archive":
		column, value = "archived", true
	default:
		return errors.New("invalid bulk action")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, value)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET "+column+"=? WHERE id IN ("+placeholders+")", args...)
	return err
}

func (s *Store) Search(ctx context.Context, query string) ([]Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id, i.url, i.canonical_url, i.title, i.author, i.site_name, i.extracted_text, i.read_state, i.snapshot_path, i.feed_id, i.published_at, i.added_at
		FROM items_fts f JOIN items i ON i.id=f.rowid WHERE items_fts MATCH ? ORDER BY rank LIMIT 100`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var item Item
		var added string
		if err := rows.Scan(&item.ID, &item.URL, &item.CanonicalURL, &item.Title, &item.Author, &item.SiteName, &item.ExtractedText, &item.ReadState, &item.SnapshotPath, &item.FeedID, &item.PublishedAt, &added); err != nil {
			return nil, err
		}
		item.AddedAt, _ = time.Parse(time.RFC3339, added)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) InsertManualItem(ctx context.Context, rawURL, title, author, siteName, content string, tags []string, readLater bool) (int64, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
	itemType, readLaterFlag, bookmarkedFlag := "link", 0, 1
	if readLater {
		itemType, readLaterFlag, bookmarkedFlag = "article", 1, 0
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO items(url, canonical_url, content_hash, title, author, site_name, source, item_type, read_later, bookmarked, added_at, extracted_text)
		VALUES (?, ?, ?, ?, ?, ?, 'manual', ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		rawURL, canonical, contentHash(title, content), title, author, siteName, itemType, readLaterFlag, bookmarkedFlag, time.Now().UTC().Format(time.RFC3339), content)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if id == 0 {
		tx.Rollback()
		return 0, errors.New("item already exists")
	}
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO tags(name) VALUES (?) ON CONFLICT(name) DO NOTHING", name); err != nil {
			tx.Rollback()
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_tags(item_id, tag_id) SELECT ?, id FROM tags WHERE name = ? COLLATE NOCASE`, id, name); err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) SetSnapshot(ctx context.Context, id int64, path, text string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE items SET snapshot_path=?, snapshot_text=? WHERE id=?", path, text, id)
	return err
}

func (s *Store) ItemIDByURL(ctx context.Context, rawURL string) (int64, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, "SELECT id FROM items WHERE canonical_url=?", canonical).Scan(&id)
	return id, err
}

func (s *Store) UserPasswordHash(ctx context.Context) (string, error) {
	var hash string
	err := s.DB.QueryRowContext(ctx, "SELECT password_hash FROM users ORDER BY id LIMIT 1").Scan(&hash)
	return hash, err
}

func (s *Store) CreateUser(ctx context.Context, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO users(password_hash, created_at) VALUES (?, ?)", passwordHash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) CreateSession(ctx context.Context, id, csrf string, expiry time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(id, user_id, csrf_token, expires_at, created_at)
		SELECT ?, id, ?, ?, ? FROM users ORDER BY id LIMIT 1`, id, csrf, expiry.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SessionCSRF(ctx context.Context, id string) (string, error) {
	var token string
	err := s.DB.QueryRowContext(ctx, "SELECT csrf_token FROM sessions WHERE id=? AND expires_at > ?", id, time.Now().UTC().Format(time.RFC3339)).Scan(&token)
	return token, err
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE id=?", id)
	return err
}

func (s *Store) LoginAllowed(ctx context.Context, address string) (bool, error) {
	var locked sql.NullString
	err := s.DB.QueryRowContext(ctx, "SELECT locked_until FROM login_attempts WHERE address=?", address).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !locked.Valid {
		return true, nil
	}
	until, err := time.Parse(time.RFC3339, locked.String)
	return err == nil && time.Now().After(until), nil
}

func (s *Store) RecordLoginFailure(ctx context.Context, address string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO login_attempts(address, failures, locked_until, updated_at) VALUES (?, 1, NULL, ?)
		ON CONFLICT(address) DO UPDATE SET failures=failures+1, locked_until=CASE WHEN failures+1 >= 5 THEN ? ELSE locked_until END, updated_at=excluded.updated_at`,
		address, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Add(15*time.Minute).Format(time.RFC3339))
	return err
}

func (s *Store) ClearLoginFailures(ctx context.Context, address string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM login_attempts WHERE address=?", address)
	return err
}

func (s *Store) CreateAPIToken(ctx context.Context, name, tokenHash, scopes string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO api_tokens(name, token_hash, scopes, created_at) VALUES (?, ?, ?, ?)`, name, tokenHash, scopes, time.Now().UTC().Format(time.RFC3339))
	return err
}

// TokenScopes returns the space-separated scopes of a live (non-revoked) token,
// or an empty string when the token is unknown or revoked.
func (s *Store) TokenScopes(ctx context.Context, tokenHash string) (string, error) {
	var scopes string
	err := s.DB.QueryRowContext(ctx, "SELECT scopes FROM api_tokens WHERE token_hash=? AND revoked_at IS NULL", tokenHash).Scan(&scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return scopes, err
}

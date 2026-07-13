package feeds

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/reader"
	"github.com/tiagojct/scrimshaw/internal/store"
)

type feedTransport string

func (r feedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/rss+xml"}},
		Body:       io.NopCloser(strings.NewReader(string(r))),
	}, nil
}

func TestAutoSnapshotCreatesSnapshotForNewFeedItem(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	feedID, err := db.AddFeed(ctx, "https://example.test/feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, "UPDATE feeds SET auto_snapshot=1 WHERE id=?", feedID); err != nil {
		t.Fatal(err)
	}
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Example</title><item><title>Story</title><link>https://example.test/story</link><description><![CDATA[<p>Story body</p>]]></description></item></channel></rss>`
	client := &fetch.Client{HTTP: &http.Client{Transport: feedTransport(rss)}}
	service := &Service{
		Store: db, Client: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DisableAfter: 5, Snapshots: &reader.Saver{Store: db, Snapshots: t.TempDir()},
	}
	if err := service.PollDue(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.ItemIDByURL(ctx, "https://example.test/story")
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.Item(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.SnapshotPath.Valid {
		t.Fatal("feed item snapshot was not stored")
	}
}

func TestPollDueFillsTitleForTitlelessEntry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.AddFeed(ctx, "https://example.test/feed", time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	// Linkblog-style feeds often publish entries with no <title>, only body text.
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Example</title><item><link>https://example.test/post</link><description><![CDATA[<p>A short note about something interesting that happened today.</p>]]></description></item></channel></rss>`
	client := &fetch.Client{HTTP: &http.Client{Transport: feedTransport(rss)}}
	service := &Service{
		Store: db, Client: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DisableAfter: 5,
	}
	if err := service.PollDue(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.ItemIDByURL(ctx, "https://example.test/post")
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.Item(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title == "" {
		t.Fatal("titleless feed entry should fall back to a derived title, not stay blank")
	}
}

func TestBackfillBlankTitles(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	id, err := db.InsertManualItem(ctx, "https://example.test/blank", "", "", "", "<p>Some archived body text here.</p>", nil, false)
	if err != nil {
		t.Fatal(err)
	}

	fixed, err := BackfillBlankTitles(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 {
		t.Fatalf("BackfillBlankTitles fixed = %d, want 1", fixed)
	}
	item, err := db.Item(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title == "" {
		t.Fatal("item title should be backfilled, not stay blank")
	}

	// Idempotent: a second run has nothing left to fix.
	if fixed, err := BackfillBlankTitles(ctx, db); err != nil || fixed != 0 {
		t.Fatalf("second BackfillBlankTitles run = (%d, %v), want (0, nil)", fixed, err)
	}
}

func TestFallbackTitle(t *testing.T) {
	if got := fallbackTitle("<p>Hello world, this is a test.</p>", "https://example.test/x"); got != "Hello world, this is a test." {
		t.Fatalf("fallbackTitle from text = %q", got)
	}
	if got := fallbackTitle("", "https://example.test/x"); got != "example.test" {
		t.Fatalf("fallbackTitle from empty text = %q, want host fallback", got)
	}
}

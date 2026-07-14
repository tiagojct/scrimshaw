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

// redirectingFeedTransport serves a fixed 301 for one URL and RSS for another,
// so a poll's permanent-redirect handling can be tested without a real
// listener (fetch.New's SSRF guard rejects loopback on principle, so
// httptest.Server isn't an option here).
type redirectingFeedTransport struct {
	from, to, rss string
}

func (t redirectingFeedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() == t.from {
		return &http.Response{StatusCode: http.StatusMovedPermanently, Header: http.Header{"Location": {t.to}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/rss+xml"}}, Body: io.NopCloser(strings.NewReader(t.rss)), Request: req}, nil
}

func TestPollDueUpdatesFeedURLAfterPermanentRedirect(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	feedID, err := db.AddFeed(ctx, "https://example.test/old-feed", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Example</title></channel></rss>`
	// fetch.New wires up the real CheckRedirect (where permanent-redirect
	// detection lives); only the Transport is swapped for a fake one.
	client := fetch.New(time.Second)
	client.HTTP.Transport = redirectingFeedTransport{from: "https://example.test/old-feed", to: "https://example.test/new-feed", rss: rss}
	service := &Service{Store: db, Client: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableAfter: 5}
	if err := service.PollDue(ctx); err != nil {
		t.Fatal(err)
	}
	feed, err := db.Feed(ctx, feedID)
	if err != nil {
		t.Fatal(err)
	}
	if feed.URL != "https://example.test/new-feed" {
		t.Fatalf("feed URL after permanent redirect = %q, want the new URL", feed.URL)
	}
}

func TestPollDueAppliesFeedContentRules(t *testing.T) {
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
	if err := db.SetFeedRules(ctx, feedID, "skip sponsored\ntag:golang go"); err != nil {
		t.Fatal(err)
	}
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Example</title>
		<item><title>Sponsored: buy our widget</title><link>https://example.test/ad</link><description>ignore this</description></item>
		<item><title>Why Go is great</title><link>https://example.test/go-post</link><description>an ordinary post</description></item>
	</channel></rss>`
	client := &fetch.Client{HTTP: &http.Client{Transport: feedTransport(rss)}}
	service := &Service{Store: db, Client: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DisableAfter: 5}
	if err := service.PollDue(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ItemIDByURL(ctx, "https://example.test/ad"); err == nil {
		t.Fatal("the skip rule should have kept the sponsored entry out of the store")
	}
	id, err := db.ItemIDByURL(ctx, "https://example.test/go-post")
	if err != nil {
		t.Fatal("the non-matching entry should have been inserted:", err)
	}
	tags, err := db.ItemTags(ctx, id)
	if err != nil || len(tags) != 1 || tags[0] != "golang" {
		t.Fatalf("tags = %v, err=%v, want just [golang]", tags, err)
	}
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

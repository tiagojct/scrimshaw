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

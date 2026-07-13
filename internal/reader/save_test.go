package reader

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/store"
)

type responseTransport string

func (r responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(string(r))),
	}, nil
}

type forbiddenTransport struct{}

func (forbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader("<html><body>blocked</body></html>")),
	}, nil
}

// Some sites (e.g. nytimes.com's DataDome bot challenge) return a 403 to any
// non-browser client regardless of User-Agent, so extraction always fails
// there. Save must still file the URL as a plain read-later link instead of
// losing the save entirely.
func TestSaveFallsBackToLinkWhenExtractionFails(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saver := &Saver{
		Store:     db,
		Client:    &fetch.Client{HTTP: &http.Client{Timeout: time.Second, Transport: forbiddenTransport{}}},
		Snapshots: t.TempDir(),
	}
	id, err := saver.Save(context.Background(), "https://www.nytimes.com/2026/07/13/opinion/blocked.html", []string{"reading"})
	if err != nil {
		t.Fatalf("Save should fall back to a link, not error: %v", err)
	}
	item, err := db.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.ReadLater {
		t.Fatal("fallback item should still be filed as read later")
	}
	if item.Title != "www.nytimes.com" {
		t.Fatalf("fallback title = %q, want host fallback", item.Title)
	}
	if item.SnapshotPath.Valid {
		t.Fatal("fallback item should not have a snapshot (no content was extracted)")
	}
}

func TestSaveExtractsAndSnapshotsSanitizedContent(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	html := `<html><head><title>Example article</title></head><body><article><p>This is enough article content to be extracted and stored safely.</p><script>alert("xss")</script></article></body></html>`
	saver := &Saver{
		Store:     db,
		Client:    &fetch.Client{HTTP: &http.Client{Timeout: time.Second, Transport: responseTransport(html)}},
		Snapshots: t.TempDir(),
	}
	id, err := saver.Save(context.Background(), "https://example.com/article", []string{"reading"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.SnapshotPath.Valid {
		t.Fatal("snapshot path was not stored")
	}
	if strings.Contains(item.ExtractedText, "<script") {
		t.Fatal("unsafe script was retained")
	}
	saved, err := os.ReadFile(item.SnapshotPath.String)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "<style>"+snapshotStyle+"</style>") {
		t.Fatal("snapshot should carry the inline reading stylesheet")
	}
	if !strings.Contains(string(saved), "Content-Security-Policy") {
		t.Fatal("snapshot should still carry its CSP")
	}
}

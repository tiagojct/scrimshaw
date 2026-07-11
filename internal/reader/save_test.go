package reader

import (
	"context"
	"io"
	"net/http"
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
}

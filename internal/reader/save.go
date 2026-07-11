package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	readability "github.com/go-shiori/go-readability"
	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/sanitize"
	"github.com/tiagojct/scrimshaw/internal/store"
)

type Saver struct {
	Store     *store.Store
	Client    *fetch.Client
	Snapshots string
}

func (s *Saver) Save(ctx context.Context, rawURL string, tags []string) (int64, error) {
	body, _, err := s.Client.Get(ctx, rawURL, "", "")
	if err != nil {
		return 0, err
	}
	parsedURL, err := store.CanonicalURL(rawURL)
	if err != nil {
		return 0, err
	}
	urlValue, err := url.Parse(parsedURL)
	if err != nil {
		return 0, err
	}
	article, err := readability.FromReader(bytes.NewReader(body), urlValue)
	if err != nil {
		return 0, fmt.Errorf("extract article: %w", err)
	}
	content := sanitize.HTML(article.Content)
	if content == "" {
		return 0, errors.New("page has no readable content")
	}
	id, err := s.Store.InsertManualItem(ctx, rawURL, article.Title, article.Byline, article.SiteName, content, tags)
	if err != nil {
		return 0, err
	}
	if err := s.SaveSnapshot(ctx, id, content, article.TextContent); err != nil {
		return 0, err
	}
	return id, nil
}

// SaveSnapshot stores already-sanitized article HTML as an offline document.
func (s *Saver) SaveSnapshot(ctx context.Context, id int64, content, text string) error {
	if err := os.MkdirAll(s.Snapshots, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.Snapshots, fmt.Sprintf("%d.html", id))
	snapshot := []byte("<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'\"></head><body>" + content + "</body></html>")
	if err := os.WriteFile(path, snapshot, 0600); err != nil {
		return err
	}
	if err := s.Store.SetSnapshot(ctx, id, path, text); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

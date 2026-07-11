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

// Save fetches, extracts, and stores a page as a Read Later article with an
// offline snapshot.
func (s *Saver) Save(ctx context.Context, rawURL string, tags []string) (int64, error) {
	title, author, siteName, content, text, err := s.extract(ctx, rawURL)
	if err != nil {
		return 0, err
	}
	id, err := s.Store.InsertManualItem(ctx, rawURL, title, author, siteName, content, tags, true)
	if err != nil {
		return 0, err
	}
	if err := s.SaveSnapshot(ctx, id, content, text); err != nil {
		return 0, err
	}
	return id, nil
}

// SaveLink stores a URL as a link-only bookmark. It fetches just enough to learn
// a title; a page that will not load is still bookmarked (its link check will
// flag it later).
func (s *Saver) SaveLink(ctx context.Context, rawURL string, tags []string) (int64, error) {
	title, author, siteName := "", "", ""
	if title2, author2, site2, _, _, err := s.extract(ctx, rawURL); err == nil {
		title, author, siteName = title2, author2, site2
	}
	if title == "" {
		if parsed, err := url.Parse(rawURL); err == nil {
			title = parsed.Host
		}
	}
	return s.Store.InsertManualItem(ctx, rawURL, title, author, siteName, "", tags, false)
}

// Extract populates an existing item's article content in place, e.g. when a
// stored bookmark is promoted to Read Later.
func (s *Saver) Extract(ctx context.Context, id int64, rawURL string) error {
	title, author, siteName, content, text, err := s.extract(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := s.Store.SetContent(ctx, id, title, author, siteName, content); err != nil {
		return err
	}
	return s.SaveSnapshot(ctx, id, content, text)
}

func (s *Saver) extract(ctx context.Context, rawURL string) (title, author, siteName, content, text string, err error) {
	body, _, err := s.Client.Get(ctx, rawURL, "", "")
	if err != nil {
		return "", "", "", "", "", err
	}
	parsedURL, err := store.CanonicalURL(rawURL)
	if err != nil {
		return "", "", "", "", "", err
	}
	urlValue, err := url.Parse(parsedURL)
	if err != nil {
		return "", "", "", "", "", err
	}
	article, err := readability.FromReader(bytes.NewReader(body), urlValue)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("extract article: %w", err)
	}
	content = sanitize.HTML(article.Content)
	if content == "" {
		return "", "", "", "", "", errors.New("page has no readable content")
	}
	return article.Title, article.Byline, article.SiteName, content, article.TextContent, nil
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

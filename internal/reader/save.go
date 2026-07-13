package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"

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
// offline snapshot. Extraction can fail for reasons unrelated to the URL being
// bad (a bot-blocked paywall like nytimes.com's DataDome challenge, a non-article
// page, a page with no readable content) — rather than losing the save entirely,
// it falls back to a plain link the same way SaveLink does, still filed as read
// later. This is the same tolerance readLaterItem already applies when promoting
// an existing bookmark and its extraction fails; a later manual retry (e.g. via
// the reader's "Read later" toggle) can attempt extraction again.
func (s *Saver) Save(ctx context.Context, rawURL string, tags []string) (int64, error) {
	title, author, siteName, content, text, err := s.extract(ctx, rawURL)
	if err != nil {
		return s.Store.InsertManualItem(ctx, rawURL, s.titleOrHost(ctx, rawURL), "", "", "", tags, true)
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

// SaveLink stores a URL as a link-only bookmark. It fetches the page to learn a
// title from <title>/og:title, which works for any page (not just articles); a
// page that will not load is still bookmarked (its link check flags it later).
func (s *Saver) SaveLink(ctx context.Context, rawURL string, tags []string) (int64, error) {
	return s.Store.InsertManualItem(ctx, rawURL, s.titleOrHost(ctx, rawURL), "", "", "", tags, false)
}

// titleOrHost fetches a page to read its <title>/og:title, falling back to the
// URL's host when the fetch fails or the page has no title.
func (s *Saver) titleOrHost(ctx context.Context, rawURL string) string {
	if body, _, err := s.Client.Get(ctx, rawURL, "", ""); err == nil {
		if title := pageTitle(body); title != "" {
			return title
		}
	}
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return rawURL
}

// pageTitle extracts a human title from an HTML page, preferring og:title /
// twitter:title over the <title> element.
func pageTitle(body []byte) string {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var docTitle, metaTitle string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if docTitle == "" && n.FirstChild != nil {
					docTitle = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				var prop, content string
				for _, a := range n.Attr {
					switch a.Key {
					case "property", "name":
						prop = a.Val
					case "content":
						content = a.Val
					}
				}
				if metaTitle == "" && (prop == "og:title" || prop == "twitter:title") {
					metaTitle = strings.TrimSpace(content)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if metaTitle != "" {
		return metaTitle
	}
	return docTitle
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

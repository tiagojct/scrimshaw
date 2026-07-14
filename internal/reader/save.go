package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	readability "github.com/go-shiori/go-readability"
	"github.com/ledongthuc/pdf"
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
	body, headers, err := s.Client.Get(ctx, rawURL, "", "")
	if err != nil {
		return "", "", "", "", "", err
	}
	if isPDF(headers.Get("Content-Type"), body) {
		return extractPDF(body)
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

// isPDF reports whether a fetched response is a PDF: Content-Type first
// (the reliable signal), falling back to the %PDF- file signature for
// servers that mislabel it.
func isPDF(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "application/pdf") {
		return true
	}
	return bytes.HasPrefix(body, []byte("%PDF-"))
}

// extractPDF turns a PDF's text into the same (title, author, siteName,
// content, text) shape extract() returns for HTML articles. Pure Go
// (github.com/ledongthuc/pdf), no external binary — the earlier PDF
// deferral (see CLAUDE.md/SPEC.md) was specifically about poppler/yt-dlp-
// style external binaries breaking the single-binary model, which doesn't
// apply to a Go module dependency. Quality caveat this doesn't try to hide:
// pure-Go extraction is well below poppler for complex layouts, and there's
// no OCR path, so a scanned/image PDF yields no text and an error here.
func extractPDF(body []byte) (title, author, siteName, content, text string, err error) {
	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("open PDF: %w", err)
	}
	textReader, err := r.GetPlainText()
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("extract PDF text: %w", err)
	}
	raw, err := io.ReadAll(textReader)
	if err != nil {
		return "", "", "", "", "", err
	}
	text = strings.TrimSpace(string(raw))
	if text == "" {
		return "", "", "", "", "", errors.New("PDF has no extractable text (likely a scanned/image PDF)")
	}
	return fallbackTitleFromText(text, 12), "", "", pdfHTML(text), text, nil
}

// pdfHTML wraps PDF-extracted plain text in escaped <p> tags (split on blank
// lines) so the reader gets real paragraphs instead of one text blob with
// literal newlines, then runs it through the same sanitizer as HTML content
// — the text is escaped first, so any "<script>"-looking sequences in a
// crafted PDF are inert text, not markup, by the time sanitize.HTML sees it.
func pdfHTML(text string) string {
	var b strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(stdhtml.EscapeString(para))
		b.WriteString("</p>")
	}
	return sanitize.HTML(b.String())
}

// fallbackTitleFromText takes the first maxWords words of text as a title —
// PDFs have no <title> element, only a real title, so the reader package
// needs its own small version of feeds.fallbackTitle's word-truncation.
func fallbackTitleFromText(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) > maxWords {
		return strings.Join(words[:maxWords], " ") + "…"
	}
	return strings.Join(words, " ")
}

// snapshotStyle is inlined (the CSP below allows no external stylesheet) so a
// snapshot opened directly in a browser, months or years later, still reads as
// a considered document rather than unstyled markup.
const snapshotStyle = `body{max-width:38rem;margin:2.5rem auto;padding:0 1.25rem;font-family:Georgia,'Times New Roman',serif;font-size:1.125rem;line-height:1.65;color:#16222a;background:#f7fafb}img{max-width:100%;height:auto}a{color:#0b4f9e}pre,code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}blockquote{margin:0 0 0 1rem;padding-left:1rem;border-left:3px solid #cdd7dc;color:#3d4a52}`

// SaveSnapshot stores already-sanitized article HTML as an offline document.
func (s *Saver) SaveSnapshot(ctx context.Context, id int64, content, text string) error {
	if err := os.MkdirAll(s.Snapshots, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.Snapshots, fmt.Sprintf("%d.html", id))
	snapshot := []byte("<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'\"><style>" + snapshotStyle + "</style></head><body>" + content + "</body></html>")
	if err := os.WriteFile(path, snapshot, 0600); err != nil {
		return err
	}
	if err := s.Store.SetSnapshot(ctx, id, path, text); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

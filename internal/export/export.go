package export

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func JSON(ctx context.Context, s *store.Store) ([]byte, error) {
	items, err := s.AllItems(ctx)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(items, "", "  ")
}

func OPML(ctx context.Context, s *store.Store) ([]byte, error) {
	feeds, err := s.AllFeeds(ctx)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>Scrimshaw feeds</title></head><body>`)
	for _, feed := range feeds {
		fmt.Fprintf(&b, `<outline type="rss" text="%s" title="%s" xmlUrl="%s"/>`, xmlEscape(feed.Title), xmlEscape(feed.Title), xmlEscape(feed.URL))
	}
	b.WriteString(`</body></opml>`)
	return []byte(b.String()), nil
}

func Markdown(ctx context.Context, s *store.Store, directory string) error {
	items, err := s.AllItems(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for _, item := range items {
		name := fmt.Sprintf("%06d-%s.md", item.ID, slug(item.Title))
		content := fmt.Sprintf("---\ntitle: %q\nurl: %q\nadded_at: %q\n---\n\n%s\n", item.Title, item.URL, item.AddedAt.Format("2006-01-02T15:04:05Z07:00"), item.ExtractedText)
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			return err
		}
	}
	return nil
}

// WeeklyDigest writes one Markdown file summarizing highlights created since
// `since` and the current starred collection. Starred items aren't
// timestamped (only a boolean flag), so unlike highlights they can't be
// scoped to "this week" — the digest lists the whole collection and says so,
// rather than implying a precision the data doesn't have. Returns the
// written path, or "" if there was nothing to report (no file is written).
func WeeklyDigest(ctx context.Context, s *store.Store, directory string, since, now time.Time) (string, error) {
	highlights, err := s.HighlightsSince(ctx, since)
	if err != nil {
		return "", err
	}
	starred, _, err := s.ListPage(ctx, store.ListOptions{State: "starred", PerPage: 200})
	if err != nil {
		return "", err
	}
	if len(highlights) == 0 && len(starred) == 0 {
		return "", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly digest — %s to %s\n\n", since.Format("2006-01-02"), now.Format("2006-01-02"))

	if len(highlights) > 0 {
		b.WriteString("## Highlights this week\n\n")
		lastItem := int64(-1)
		for _, h := range highlights {
			if h.ItemID != lastItem {
				fmt.Fprintf(&b, "### [%s](%s)\n\n", h.ItemTitle, h.ItemURL)
				lastItem = h.ItemID
			}
			if h.Quote != "" {
				fmt.Fprintf(&b, "> %s\n\n", h.Quote)
			}
			if h.Note != "" {
				fmt.Fprintf(&b, "%s\n\n", h.Note)
			}
		}
	}

	b.WriteString("## Starred\n\n")
	if len(starred) == 0 {
		b.WriteString("Nothing starred yet.\n")
	} else {
		for _, item := range starred {
			fmt.Fprintf(&b, "- [%s](%s)\n", item.Title, item.URL)
		}
	}

	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, fmt.Sprintf("digest-%s.md", now.Format("2006-01-02")))
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(value)
}
func slug(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

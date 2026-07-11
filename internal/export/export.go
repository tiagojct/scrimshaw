package export

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

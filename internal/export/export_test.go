package export

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestWeeklyDigestSkipsWritingWhenNothingToReport(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dir := t.TempDir()
	now := time.Now().UTC()
	path, err := WeeklyDigest(ctx, s, dir, now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty (nothing to report)", path)
	}
	entries, err := os.ReadDir(dir)
	// dir may not even exist yet, since WeeklyDigest returns before MkdirAll
	// when there's nothing to write.
	if err == nil && len(entries) != 0 {
		t.Fatalf("expected no files written, got %v", entries)
	}
}

func TestWeeklyDigestIncludesHighlightsAndStarred(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	recentID, err := s.InsertManualItem(ctx, "https://ex/recent", "Recent Article", "", "", "<p>body</p>", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddHighlight(ctx, recentID, "a memorable quote", "my note", 0); err != nil {
		t.Fatal(err)
	}

	oldID, err := s.InsertManualItem(ctx, "https://ex/old", "Old Article", "", "", "<p>body</p>", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	oldQuoteTime := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO highlights(item_id, quote, note, position, created_at) VALUES (?, 'stale quote', '', 0, ?)", oldID, oldQuoteTime); err != nil {
		t.Fatal(err)
	}

	starredID, err := s.InsertManualItem(ctx, "https://ex/starred", "Starred Article", "", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStarred(ctx, starredID, true); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	now := time.Now().UTC()
	path, err := WeeklyDigest(ctx, s, dir, now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("expected a digest to be written")
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("path = %q, want inside %q", path, dir)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)
	if !strings.Contains(body, "Recent Article") || !strings.Contains(body, "a memorable quote") || !strings.Contains(body, "my note") {
		t.Fatalf("digest missing the recent highlight: %s", body)
	}
	if strings.Contains(body, "stale quote") || strings.Contains(body, "Old Article") {
		t.Fatalf("digest should not include a highlight from 30 days ago: %s", body)
	}
	if !strings.Contains(body, "Starred Article") {
		t.Fatalf("digest missing the starred item: %s", body)
	}
}

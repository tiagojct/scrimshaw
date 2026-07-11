package importers

import (
	"context"
	"strings"
	"testing"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestLinkdingImportsPaginatedAPIResponse(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data := `{"results":[{"url":"https://example.com/a","title":"Article","tag_names":["research"],"unread":true}]}`
	count, err := Linkding(context.Background(), strings.NewReader(data), db)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	items, total, err := db.ListPage(context.Background(), store.ListOptions{Tag: "research", State: "unread"})
	if err != nil || total != 1 || items[0].Title != "Article" {
		t.Fatalf("items=%v total=%d err=%v", items, total, err)
	}
}

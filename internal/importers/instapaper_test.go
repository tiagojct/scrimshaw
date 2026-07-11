package importers

import (
	"context"
	"strings"
	"testing"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestInstapaperImportsFolderAndStar(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count, err := Instapaper(context.Background(), strings.NewReader("URL,Title,Folder,Starred\nhttps://example.com/a,Article,Long reads,true\n"), db)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	items, total, err := db.ListPage(context.Background(), store.ListOptions{Tag: "Long reads"})
	if err != nil || total != 1 || items[0].Title != "Article" {
		t.Fatalf("items=%v total=%d err=%v", items, total, err)
	}
}

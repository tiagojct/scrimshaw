package backup

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestRunOnceCreatesAValidBackup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/scrimshaw.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.InsertManualItem(ctx, "https://ex/a", "An item", "", "", "", nil, false); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	svc := &Service{Store: db, Dir: dir, Keep: 7, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one backup file, got %d", len(entries))
	}

	backupDB, err := store.Open(ctx, filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("backup file is not a valid SQLite database: %v", err)
	}
	defer backupDB.Close()
	item, err := backupDB.Item(ctx, 1)
	if err != nil || item.Title != "An item" {
		t.Fatalf("backup does not contain the original data: item=%v err=%v", item, err)
	}
}

func TestPruneKeepsOnlyTheMostRecentBackups(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"scrimshaw-20260101-000000.000000001.db",
		"scrimshaw-20260102-000000.000000001.db",
		"scrimshaw-20260103-000000.000000001.db",
		"scrimshaw-20260104-000000.000000001.db",
		"not-a-backup.txt",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Dir: dir, Keep: 2}
	if err := svc.prune(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var remaining []string
	for _, e := range entries {
		remaining = append(remaining, e.Name())
	}
	if len(remaining) != 3 { // 2 kept backups + the untouched non-backup file
		t.Fatalf("remaining files = %v", remaining)
	}
	for _, want := range []string{"scrimshaw-20260103-000000.000000001.db", "scrimshaw-20260104-000000.000000001.db", "not-a-backup.txt"} {
		found := false
		for _, got := range remaining {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %q to remain, remaining = %v", want, remaining)
		}
	}
}

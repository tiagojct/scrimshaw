// Package backup periodically snapshots the database via SQLite's online
// VACUUM INTO, rotating out old snapshots, so a working backup exists even if
// nobody sets up an external cron job.
package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tiagojct/scrimshaw/internal/store"
)

const filePrefix = "scrimshaw-"

type Service struct {
	Store  *store.Store
	Dir    string
	Keep   int // backups kept on disk; non-positive defaults to 7
	Logger *slog.Logger
}

// RunOnce takes one VACUUM INTO snapshot and prunes backups beyond Keep.
func (s *Service) RunOnce(ctx context.Context) error {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	// Nanosecond precision (not just seconds) so two backups triggered within
	// the same second — a manual re-trigger, or a fast test loop — never
	// collide on the same filename, which VACUUM INTO would refuse to overwrite.
	name := fmt.Sprintf("%s%s.db", filePrefix, time.Now().UTC().Format("20060102-150405.000000000"))
	if err := s.Store.BackupTo(ctx, filepath.Join(s.Dir, name)); err != nil {
		return err
	}
	return s.prune()
}

// prune deletes the oldest backups beyond Keep. Filenames carry a sortable
// UTC timestamp, so a plain lexicographic sort is also chronological order.
func (s *Service) prune() error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), filePrefix) && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	keep := s.Keep
	if keep <= 0 {
		keep = 7
	}
	for len(names) > keep {
		if err := os.Remove(filepath.Join(s.Dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// Run takes a backup immediately, then again on every interval, until ctx is
// canceled — the same ticker pattern as feeds.Service.Run and links.Checker.Run.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("backup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

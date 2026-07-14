// Package digest periodically writes a Markdown summary of the past week's
// highlights and starred items, so there's a passive weekly review without
// needing to open the app and go looking.
package digest

import (
	"context"
	"log/slog"
	"time"

	"github.com/tiagojct/scrimshaw/internal/export"
	"github.com/tiagojct/scrimshaw/internal/store"
)

// Service writes a weekly digest file. Unlike feeds/links/backup, it's fine
// for this to run first at startup even mid-week: it always covers "the last
// 7 days" as a rolling window, not a calendar week, so there's no catch-up
// or reconciliation logic needed — just the same ticker pattern.
type Service struct {
	Store     *store.Store
	Directory string
	Logger    *slog.Logger
}

// RunOnce writes one digest covering the last 7 days, or does nothing (and
// says so in the log) if there's nothing to report.
func (s *Service) RunOnce(ctx context.Context) error {
	now := time.Now().UTC()
	path, err := export.WeeklyDigest(ctx, s.Store, s.Directory, now.AddDate(0, 0, -7), now)
	if err != nil {
		return err
	}
	if path == "" {
		s.Logger.Info("weekly digest: nothing to report, skipped")
		return nil
	}
	s.Logger.Info("weekly digest written", "path", path)
	return nil
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("weekly digest failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

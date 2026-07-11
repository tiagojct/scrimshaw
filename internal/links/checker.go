// Package links periodically verifies that stored bookmarks still resolve, so
// the interface can flag dead links.
package links

import (
	"context"
	"log/slog"
	"time"

	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/store"
)

type Checker struct {
	Store  *store.Store
	Client *fetch.Client
	Logger *slog.Logger
	Batch  int           // links checked per pass
	MaxAge time.Duration // recheck a link once it is this stale
}

// CheckDue verifies a batch of the least-recently-checked links.
func (c *Checker) CheckDue(ctx context.Context) error {
	batch := c.Batch
	if batch <= 0 {
		batch = 20
	}
	maxAge := c.MaxAge
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	items, err := c.Store.LinksToCheck(ctx, time.Now().Add(-maxAge), batch)
	if err != nil {
		return err
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, err := c.Client.Status(ctx, item.URL)
		if err != nil {
			status = -1 // transport error: unreachable host, TLS failure, timeout
		}
		if err := c.Store.SetLinkStatus(ctx, item.ID, status); err != nil {
			c.Logger.Warn("record link status failed", "item", item.ID, "error", err)
		}
	}
	return nil
}

func (c *Checker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.CheckDue(ctx); err != nil && ctx.Err() == nil {
			c.Logger.Error("link check pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

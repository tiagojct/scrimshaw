package feeds

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/mmcdole/gofeed"
	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/reader"
	"github.com/tiagojct/scrimshaw/internal/sanitize"
	"github.com/tiagojct/scrimshaw/internal/store"
)

type Service struct {
	Store        *store.Store
	Client       *fetch.Client
	Logger       *slog.Logger
	DisableAfter int
	Snapshots    *reader.Saver
}

func (s *Service) PollDue(ctx context.Context) error {
	feeds, err := s.Store.DueFeeds(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, feed := range feeds {
		if err := s.pollOne(ctx, feed); err != nil {
			s.Logger.Warn("feed poll failed", "feed_id", feed.ID, "url", feed.URL, "error", err)
			if recordErr := s.Store.RecordFeedFailure(ctx, feed, err, s.DisableAfter); recordErr != nil {
				return recordErr
			}
		}
	}
	return nil
}

func (s *Service) pollOne(ctx context.Context, feed store.Feed) error {
	body, headers, err := s.Client.Get(ctx, feed.URL, feed.ETag, feed.LastModified)
	if err != nil {
		return err
	}
	if body == nil {
		return s.Store.RecordFeedSuccess(ctx, feed, feed.Title, headers.Get("ETag"), headers.Get("Last-Modified"))
	}
	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		return fmt.Errorf("parse feed: %w", err)
	}
	for _, entry := range parsed.Items {
		if entry.Link == "" {
			continue
		}
		text := entry.Description
		if entry.Content != "" {
			text = entry.Content
		}
		if feed.FetchFullContent {
			if extracted, err := s.extractFullContent(ctx, entry.Link); err != nil {
				s.Logger.Warn("full feed item extraction failed; retaining feed content", "feed_id", feed.ID, "url", entry.Link, "error", err)
			} else {
				text = extracted
			}
		}
		published := entry.PublishedParsed
		if published == nil {
			published = entry.UpdatedParsed
		}

		var publishedAt time.Time
		if published != nil {
			publishedAt = *published
		}
		author := ""
		if entry.Author != nil {
			author = entry.Author.Name
		}
		inserted, err := s.Store.InsertFeedItem(ctx, feed.ID, entry.Link, entry.Title, author, text, publishedAt)
		if err != nil {
			return fmt.Errorf("store feed item: %w", err)
		}
		if inserted && feed.AutoSnapshot && s.Snapshots != nil {
			itemID, err := s.Store.ItemIDByURL(ctx, entry.Link)
			if err != nil {
				return fmt.Errorf("find newly inserted feed item: %w", err)
			}
			content := sanitize.HTML(text)
			if err := s.Snapshots.SaveSnapshot(ctx, itemID, content, text); err != nil {
				return fmt.Errorf("snapshot feed item: %w", err)
			}
		}
	}
	return s.Store.RecordFeedSuccess(ctx, feed, parsed.Title, headers.Get("ETag"), headers.Get("Last-Modified"))
}

func (s *Service) extractFullContent(ctx context.Context, rawURL string) (string, error) {
	body, _, err := s.Client.Get(ctx, rawURL, "", "")
	if err != nil {
		return "", err
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	article, err := readability.FromReader(bytes.NewReader(body), parsedURL)
	if err != nil {
		return "", err
	}
	content := sanitize.HTML(article.Content)
	if content == "" {
		return "", fmt.Errorf("extraction returned empty content")
	}
	return content, nil
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.PollDue(ctx); err != nil {
			s.Logger.Error("feed polling pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

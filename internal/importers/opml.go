package importers

import (
	"context"
	"encoding/xml"
	"io"
	"time"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func OPML(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	var document struct {
		Outlines []outline `xml:"body>outline"`
	}
	if err := xml.NewDecoder(io.LimitReader(input, 10<<20)).Decode(&document); err != nil {
		return 0, err
	}
	var imported int
	var walk func([]outline) error
	walk = func(outlines []outline) error {
		for _, entry := range outlines {
			if entry.XMLURL != "" {
				if _, err := destination.AddFeed(ctx, entry.XMLURL, time.Hour, nil); err == nil {
					imported++
				}
			}
			if err := walk(entry.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return imported, walk(document.Outlines)
}

type outline struct {
	XMLURL   string    `xml:"xmlUrl,attr"`
	Children []outline `xml:"outline"`
}

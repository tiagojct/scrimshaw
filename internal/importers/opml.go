package importers

import (
	"context"
	"encoding/xml"
	"io"
	"time"

	"github.com/tiagojct/scrimshaw/internal/store"
)

func OPML(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	feeds, err := ParseOPML(input)
	if err != nil {
		return 0, err
	}
	imported := 0
	for _, f := range feeds {
		if _, err := destination.AddFeed(ctx, f.URL, time.Hour, nil); err == nil {
			imported++
		}
	}
	return imported, nil
}

// OPMLFeed is a subscription discovered in an OPML file.
type OPMLFeed struct {
	Title string
	URL   string
}

// ParseOPML extracts the feed subscriptions from an OPML document without
// inserting anything, so the caller can preview and tag them first.
func ParseOPML(input io.Reader) ([]OPMLFeed, error) {
	var document struct {
		Outlines []outline `xml:"body>outline"`
	}
	if err := xml.NewDecoder(io.LimitReader(input, 10<<20)).Decode(&document); err != nil {
		return nil, err
	}
	var feeds []OPMLFeed
	var walk func([]outline)
	walk = func(outlines []outline) {
		for _, entry := range outlines {
			if entry.XMLURL != "" {
				title := entry.Title
				if title == "" {
					title = entry.Text
				}
				feeds = append(feeds, OPMLFeed{Title: title, URL: entry.XMLURL})
			}
			walk(entry.Children)
		}
	}
	walk(document.Outlines)
	return feeds, nil
}

type outline struct {
	XMLURL   string    `xml:"xmlUrl,attr"`
	Title    string    `xml:"title,attr"`
	Text     string    `xml:"text,attr"`
	Children []outline `xml:"outline"`
}

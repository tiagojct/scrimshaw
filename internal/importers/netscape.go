package importers

import (
	"context"
	"io"
	"strings"

	"golang.org/x/net/html"

	"github.com/tiagojct/scrimshaw/internal/store"
)

// NetscapeBookmarks imports standard bookmark-export HTML. Folder headings become
// flat tags, preserving Scrimshaw's no-folder invariant.
func NetscapeBookmarks(ctx context.Context, input io.Reader, destination *store.Store) (int, error) {
	root, err := html.Parse(input)
	if err != nil {
		return 0, err
	}
	var imported int
	var walk func(*html.Node, []string) error
	walk = func(node *html.Node, tags []string) error {
		if node.Type == html.ElementNode && node.Data == "a" {
			rawURL := attribute(node, "href")
			if rawURL != "" {
				if _, err := destination.InsertManualItem(ctx, rawURL, strings.TrimSpace(text(node)), "", "", "", tags, false); err == nil {
					imported++
				}
			}
		}
		activeTags := tags
		if node.Type == html.ElementNode && node.Data == "dt" {
			if heading := firstElement(node, "h3"); heading != nil {
				name := strings.TrimSpace(text(heading))
				if name != "" {
					activeTags = append(append([]string{}, tags...), name)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if node.Type == html.ElementNode && node.Data == "dl" && child.Type == html.ElementNode && child.Data == "dt" {
				if heading := firstElement(child, "h3"); heading != nil {
					name := strings.TrimSpace(text(heading))
					if name != "" {
						activeTags = append(append([]string{}, tags...), name)
					}
				}
			}
			childTags := tags
			if node.Type == html.ElementNode && node.Data == "dt" || node.Type == html.ElementNode && node.Data == "dl" && child.Type == html.ElementNode && child.Data == "dl" {
				childTags = activeTags
			}
			if err := walk(child, childTags); err != nil {
				return err
			}
		}
		return nil
	}
	return imported, walk(root, nil)
}

func firstElement(node *html.Node, name string) *html.Node {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == name {
			return child
		}
	}
	return nil
}

func attribute(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val
		}
	}
	return ""
}

func text(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, "")
}

package feeds

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/tiagojct/scrimshaw/internal/fetch"
)

// DiscoverFavicon finds a feed's site favicon: first by parsing
// <link rel="icon"|"shortcut icon"> from the site's homepage, falling back
// to the conventional /favicon.ico path. Both candidates are confirmed with
// a Status check before being returned, so a stored favicon_url is never a
// URL known to 404. Returns "" (never an error) on any failure — the caller
// treats discovery as best-effort and falls back to a generated monogram.
func DiscoverFavicon(ctx context.Context, client *fetch.Client, siteOrFeedURL string) string {
	origin, err := siteOrigin(siteOrFeedURL)
	if err != nil {
		return ""
	}
	if body, _, err := client.Get(ctx, origin, "", ""); err == nil {
		if href := findFaviconLink(body); href != "" {
			if resolved, err := resolveURL(origin, href); err == nil && confirmExists(ctx, client, resolved) {
				return resolved
			}
		}
	}
	fallback := origin + "/favicon.ico"
	if confirmExists(ctx, client, fallback) {
		return fallback
	}
	return ""
}

func confirmExists(ctx context.Context, client *fetch.Client, rawURL string) bool {
	status, err := client.Status(ctx, rawURL)
	return err == nil && status >= 200 && status < 300
}

func siteOrigin(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// findFaviconLink returns the first <link rel="icon"> (or "shortcut icon")
// href found in an HTML document, or "" if there isn't one.
func findFaviconLink(body []byte) string {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	var href string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if href != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "link" {
			var rel, h string
			for _, a := range n.Attr {
				switch a.Key {
				case "rel":
					rel = strings.ToLower(a.Val)
				case "href":
					h = a.Val
				}
			}
			if h != "" && strings.Contains(rel, "icon") {
				href = h
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
			if href != "" {
				return
			}
		}
	}
	walk(root)
	return href
}

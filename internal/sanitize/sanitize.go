package sanitize

import (
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var policy = buildPolicy()

func buildPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	// Keep lazy-load hints and srcset so rewriteImages can recover a real source
	// before they are stripped; it collapses them to a single proxied src.
	p.AllowAttrs("src", "srcset", "data-src", "data-original", "data-lazy-src", "data-srcset", "alt", "title").OnElements("img")
	return p
}

// HTML removes executable markup and routes remote images through the local proxy.
func HTML(input string) string {
	root, err := html.Parse(strings.NewReader(policy.Sanitize(input)))
	if err != nil {
		return ""
	}
	rewriteImages(root)
	var output strings.Builder
	if err := html.Render(&output, root); err != nil {
		return ""
	}
	return output.String()
}

// rewriteImages resolves each <img> to a single fetchable source (handling
// protocol-relative URLs, lazy-load attributes, and srcset) and routes it
// through the local image proxy, dropping the source entirely if none resolves.
func rewriteImages(node *html.Node) {
	if node.Type == html.ElementNode && node.Data == "img" {
		alt, title := attr(node, "alt"), attr(node, "title")
		best := bestSource(node)
		node.Attr = node.Attr[:0]
		if proxied := proxyURL(best); proxied != "" {
			node.Attr = append(node.Attr,
				html.Attribute{Key: "src", Val: proxied},
				html.Attribute{Key: "loading", Val: "lazy"})
		}
		if alt != "" {
			node.Attr = append(node.Attr, html.Attribute{Key: "alt", Val: alt})
		}
		if title != "" {
			node.Attr = append(node.Attr, html.Attribute{Key: "title", Val: title})
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		rewriteImages(child)
	}
}

func bestSource(node *html.Node) string {
	for _, key := range []string{"src", "data-src", "data-original", "data-lazy-src"} {
		if v := attr(node, key); v != "" && !strings.HasPrefix(v, "data:") {
			return v
		}
	}
	if v := firstSrcset(attr(node, "srcset")); v != "" {
		return v
	}
	return firstSrcset(attr(node, "data-srcset"))
}

// firstSrcset returns the first URL of a srcset attribute value.
func firstSrcset(value string) string {
	if value == "" {
		return ""
	}
	first := strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
	return strings.TrimSpace(strings.SplitN(first, " ", 2)[0])
}

// proxyURL normalizes a candidate image URL (accepting protocol-relative forms)
// and rewrites it to the local proxy, or returns "" when it is not fetchable.
func proxyURL(raw string) string {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return "/images?url=" + url.QueryEscape(parsed.String())
}

func attr(node *html.Node, key string) string {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

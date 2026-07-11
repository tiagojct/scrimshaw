package sanitize

import (
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var policy = bluemonday.UGCPolicy()

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

func rewriteImages(node *html.Node) {
	if node.Type == html.ElementNode && node.Data == "img" {
		for index := range node.Attr {
			if node.Attr[index].Key != "src" {
				continue
			}
			source, err := url.Parse(node.Attr[index].Val)
			if err != nil || (source.Scheme != "http" && source.Scheme != "https") {
				node.Attr = append(node.Attr[:index], node.Attr[index+1:]...)
				break
			}
			node.Attr[index].Val = "/images?url=" + url.QueryEscape(source.String())
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		rewriteImages(child)
	}
}

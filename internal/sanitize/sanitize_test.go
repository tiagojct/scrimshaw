package sanitize

import (
	"strings"
	"testing"
)

func TestHTMLStripsScriptsAndProxiesRemoteImages(t *testing.T) {
	output := HTML(`<p>Hello</p><script>alert(1)</script><img src="https://images.example.test/pic.png">`)
	if strings.Contains(output, "<script") {
		t.Fatal("script was retained")
	}
	if !strings.Contains(output, `/images?url=https%3A%2F%2Fimages.example.test%2Fpic.png`) {
		t.Fatalf("image was not proxied: %s", output)
	}
}

func TestHTMLRecoversProtocolRelativeAndLazyImages(t *testing.T) {
	// Protocol-relative URLs (common on CDNs) must be proxied, not dropped.
	if out := HTML(`<img src="//cdn.example.test/a.jpg" alt="x">`); !strings.Contains(out, `/images?url=https%3A%2F%2Fcdn.example.test%2Fa.jpg`) {
		t.Fatalf("protocol-relative image not recovered: %s", out)
	}
	// A lazy-loaded image (placeholder src + real data-src) must use the real source.
	if out := HTML(`<img src="data:image/gif;base64,AAAA" data-src="https://cdn.example.test/real.jpg" alt="x">`); !strings.Contains(out, `/images?url=https%3A%2F%2Fcdn.example.test%2Freal.jpg`) {
		t.Fatalf("lazy image not recovered: %s", out)
	}
	// srcset provides the source when there is no usable src.
	if out := HTML(`<img srcset="//cdn.example.test/small.jpg 480w, //cdn.example.test/large.jpg 1200w">`); !strings.Contains(out, `/images?url=https%3A%2F%2Fcdn.example.test%2Fsmall.jpg`) {
		t.Fatalf("srcset image not recovered: %s", out)
	}
}

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

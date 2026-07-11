package reader

import "testing"

func TestPageTitle(t *testing.T) {
	if got := pageTitle([]byte(`<html><head><title>Plain Title</title></head></html>`)); got != "Plain Title" {
		t.Fatalf("title tag: %q", got)
	}
	if got := pageTitle([]byte(`<html><head><meta property="og:title" content="Rich Title"><title>Plain</title></head></html>`)); got != "Rich Title" {
		t.Fatalf("og:title preferred: %q", got)
	}
	if got := pageTitle([]byte(`<html><head></head><body>no title</body></html>`)); got != "" {
		t.Fatalf("no title: %q", got)
	}
}

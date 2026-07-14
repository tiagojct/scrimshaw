package feeds

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tiagojct/scrimshaw/internal/fetch"
)

// faviconTransport serves fixed responses keyed by exact URL, so discovery
// can be tested without a real listener (fetch.New's SSRF guard rejects
// loopback on principle, so httptest.Server isn't an option here).
type faviconTransport map[string]struct {
	status int
	body   string
}

func (t faviconTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, ok := t[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return &http.Response{StatusCode: resp.status, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(resp.body)), Request: req}, nil
}

func TestDiscoverFaviconPrefersLinkTag(t *testing.T) {
	client := fetch.New(time.Second)
	client.HTTP.Transport = faviconTransport{
		"https://example.test": {200, `<html><head><link rel="shortcut icon" href="/assets/icon.png"></head></html>`},
		"https://example.test/assets/icon.png": {200, ""},
	}
	got := DiscoverFavicon(context.Background(), client, "https://example.test/feed.xml")
	if got != "https://example.test/assets/icon.png" {
		t.Fatalf("DiscoverFavicon = %q", got)
	}
}

func TestDiscoverFaviconFallsBackToConventionalPath(t *testing.T) {
	client := fetch.New(time.Second)
	client.HTTP.Transport = faviconTransport{
		"https://example.test":              {200, `<html><head><title>no icon link here</title></head></html>`},
		"https://example.test/favicon.ico": {200, ""},
	}
	got := DiscoverFavicon(context.Background(), client, "https://example.test/feed.xml")
	if got != "https://example.test/favicon.ico" {
		t.Fatalf("DiscoverFavicon = %q", got)
	}
}

func TestDiscoverFaviconReturnsEmptyWhenNothingFound(t *testing.T) {
	client := fetch.New(time.Second)
	client.HTTP.Transport = faviconTransport{
		"https://example.test": {200, `<html><head></head></html>`},
	}
	if got := DiscoverFavicon(context.Background(), client, "https://example.test/feed.xml"); got != "" {
		t.Fatalf("DiscoverFavicon = %q, want empty", got)
	}
}

func TestDiscoverFaviconDoesNotTrustA404dLinkTagTarget(t *testing.T) {
	client := fetch.New(time.Second)
	client.HTTP.Transport = faviconTransport{
		"https://example.test": {200, `<html><head><link rel="icon" href="/missing.png"></head></html>`},
		"https://example.test/missing.png": {404, ""},
		"https://example.test/favicon.ico": {200, ""},
	}
	got := DiscoverFavicon(context.Background(), client, "https://example.test/feed.xml")
	if got != "https://example.test/favicon.ico" {
		t.Fatalf("DiscoverFavicon = %q, want the conventional fallback since the linked icon 404s", got)
	}
}

func TestFindFaviconLink(t *testing.T) {
	html := `<html><head><link rel="stylesheet" href="/x.css"><link rel="icon" type="image/png" href="/icon.png"></head></html>`
	if got := findFaviconLink([]byte(html)); got != "/icon.png" {
		t.Fatalf("findFaviconLink = %q", got)
	}
	if got := findFaviconLink([]byte(`<html><head></head></html>`)); got != "" {
		t.Fatalf("findFaviconLink = %q, want empty", got)
	}
}

package fetch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// redirectingTransport serves a fixed redirect chain ending in a 200, so
// GetTrackingRedirects can be tested without a real listener (httptest.Server
// binds to loopback, which the SSRF guard already rejects on principle).
type redirectingTransport struct {
	// hops maps a requested URL to (status, Location) for a redirect, or to
	// (200, "") for the final response.
	hops map[string][2]string
}

func (t redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hop, ok := t.hops[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
	}
	status, location := hop[0], hop[1]
	header := http.Header{"Content-Type": {"text/html"}}
	if location != "" {
		header.Set("Location", location)
	}
	code := http.StatusOK
	switch status {
	case "301":
		code = http.StatusMovedPermanently
	case "302":
		code = http.StatusFound
	}
	// A real *http.Transport always sets Response.Request to the request it
	// answered; net/http's Client relies on that (not on its own bookkeeping)
	// to know the final request's URL, so a fake RoundTripper must too.
	return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
}

func TestGetTrackingRedirectsReportsPermanentRedirect(t *testing.T) {
	client := &Client{HTTP: &http.Client{
		CheckRedirect: checkRedirect,
		Transport: redirectingTransport{hops: map[string][2]string{
			"https://old.example.test/feed": {"301", "https://new.example.test/feed"},
			"https://new.example.test/feed": {"200", ""},
		}},
	}}
	_, _, finalURL, permanent, err := client.GetTrackingRedirects(context.Background(), "https://old.example.test/feed", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !permanent {
		t.Fatal("expected a permanent redirect to be reported")
	}
	if finalURL != "https://new.example.test/feed" {
		t.Fatalf("finalURL = %q", finalURL)
	}
}

func TestGetTrackingRedirectsIgnoresTemporaryRedirect(t *testing.T) {
	client := &Client{HTTP: &http.Client{
		CheckRedirect: checkRedirect,
		Transport: redirectingTransport{hops: map[string][2]string{
			"https://stable.example.test/feed": {"302", "https://stable.example.test/feed-tmp"},
			"https://stable.example.test/feed-tmp": {"200", ""},
		}},
	}}
	_, _, finalURL, permanent, err := client.GetTrackingRedirects(context.Background(), "https://stable.example.test/feed", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if permanent {
		t.Fatal("a 302 should not be reported as permanent")
	}
	if finalURL != "https://stable.example.test/feed-tmp" {
		t.Fatalf("finalURL = %q", finalURL)
	}
}

func TestValidateHostRejectsNonPublicAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "100.64.0.1", "169.254.1.1"} {
		if err := ValidateHost(context.Background(), host); err == nil {
			t.Errorf("ValidateHost(%q) succeeded", host)
		}
	}
	if err := ValidateURL("file:///etc/passwd"); err == nil {
		t.Error("file URL succeeded")
	}
}

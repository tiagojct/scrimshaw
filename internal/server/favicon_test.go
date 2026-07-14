package server

import (
	"strings"
	"testing"
)

func TestFeedIconPrefersRealFaviconOverMonogram(t *testing.T) {
	html := feedIcon("Ars Technica", "https://arstechnica.com/favicon.ico")
	if !strings.Contains(html, `<img`) || !strings.Contains(html, "url=https%3A%2F%2Farstechnica.com%2Ffavicon.ico") {
		t.Fatalf("expected an <img> proxied through /images, got %s", html)
	}
}

func TestFeedIconMonogramFallback(t *testing.T) {
	html := feedIcon("Ars Technica", "")
	if strings.Contains(html, "<img") {
		t.Fatalf("should not render an <img> with no favicon URL: %s", html)
	}
	if !strings.Contains(html, ">A</span>") {
		t.Fatalf("monogram should use the first letter of the title: %s", html)
	}
	// Same hash palette as tags, for visual consistency.
	if !strings.Contains(html, "favicon-mono tag-c") {
		t.Fatalf("monogram should reuse the tag-chip palette classes: %s", html)
	}
}

func TestFeedIconMonogramFallsBackToQuestionMarkForEmptyTitle(t *testing.T) {
	if html := feedIcon("", ""); !strings.Contains(html, ">?</span>") {
		t.Fatalf("expected a ? placeholder for an empty title, got %s", html)
	}
}

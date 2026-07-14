package server

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The content is HTML embedded as escaped text (type="html" means "unescape
// this text to get HTML", not "this element contains live XML markup") — a
// hand-built string would be easy to get subtly wrong, so this locks down
// that encoding/xml's chardata escaping does the right thing.
func TestAtomFeedMarshalsHTMLContentAsEscapedText(t *testing.T) {
	feed := atomFeed{
		Title:   "Scrimshaw — Shared",
		ID:      "https://example.test/feed.xml",
		Updated: "2026-01-01T00:00:00Z",
		Links:   []atomLink{{Href: "https://example.test/"}, {Href: "https://example.test/feed.xml?token=x", Rel: "self"}},
		Entries: []atomEntry{{
			Title:     "An <em>emphatic</em> title & more",
			ID:        "https://example.test/a",
			Link:      atomLink{Href: "https://example.test/a"},
			Published: "2026-01-01T00:00:00Z",
			Updated:   "2026-01-01T00:00:00Z",
			Author:    &atomAuthor{Name: "Jane Doe"},
			Content:   atomContent{Type: "html", Body: `<p>Hello & <script>alert(1)</script></p>`},
		}},
	}
	out, err := xml.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)

	if !strings.Contains(body, `xmlns="http://www.w3.org/2005/Atom"`) {
		t.Fatalf("missing Atom namespace: %s", body)
	}
	if !strings.Contains(body, `rel="self"`) {
		t.Fatalf("self link missing rel attribute: %s", body)
	}
	// The raw HTML must never appear as live markup in the XML — only as
	// escaped text, since type="html" content is XML-escaped HTML source.
	if strings.Contains(body, "<script>") || strings.Contains(body, "<p>Hello") {
		t.Fatalf("HTML content leaked as live XML markup: %s", body)
	}
	if !strings.Contains(body, "&lt;p&gt;Hello &amp; &lt;script&gt;") {
		t.Fatalf("HTML content should be present as escaped text: %s", body)
	}
	if !strings.Contains(body, "An &lt;em&gt;emphatic&lt;/em&gt; title &amp; more") {
		t.Fatalf("entry title should be escaped too: %s", body)
	}

	// Round-trip: unmarshaling should recover the exact original HTML.
	var round atomFeed
	if err := xml.Unmarshal(out, &round); err != nil {
		t.Fatal(err)
	}
	if round.Entries[0].Content.Body != feed.Entries[0].Content.Body {
		t.Fatalf("round-tripped content = %q, want %q", round.Entries[0].Content.Body, feed.Entries[0].Content.Body)
	}
}

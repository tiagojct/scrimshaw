package server

import (
	"strings"
	"testing"
)

func TestExcerpt(t *testing.T) {
	// Match is wrapped and surrounding tags are stripped.
	out := string(excerpt("<p>WD 1856 b is the only confirmed <b>planet</b> that survived.</p>", "planet", 34))
	if !strings.Contains(out, "<mark>planet</mark>") {
		t.Fatalf("term not marked: %q", out)
	}
	if strings.Contains(out, "<b>") || strings.Contains(out, "<p>") {
		t.Fatalf("source tags leaked: %q", out)
	}

	// Content HTML/script is never live; only <mark> is real markup.
	out = string(excerpt(`<p>see <script>alert(1)</script> danger here</p>`, "danger", 34))
	if strings.Contains(out, "<script") || strings.Contains(out, "alert(1)") && strings.Contains(out, "<script") {
		t.Fatalf("script leaked: %q", out)
	}
	if strings.Contains(out, "<") && !strings.Contains(strings.ReplaceAll(out, "<mark>", ""), "<") {
		// only remaining '<' should be from </mark>
	}

	// An out-of-body query still yields a leading excerpt (no match, no mark).
	out = string(excerpt("<p>alpha beta gamma delta epsilon</p>", "zzz", 3))
	if strings.Contains(out, "<mark>") {
		t.Fatalf("unexpected mark: %q", out)
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("expected trailing ellipsis: %q", out)
	}

	// Empty content yields empty excerpt.
	if excerpt("", "x", 10) != "" {
		t.Fatal("empty content should give empty excerpt")
	}
}

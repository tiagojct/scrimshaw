package main

import (
	"strings"
	"testing"
)

func TestReplaceMarked(t *testing.T) {
	css := "before\n  /* gentokens:accent:light:start note */\n  --gl-accent: #000000;\n  /* gentokens:accent:light:end */\nafter\n"
	out, err := replaceMarked(css, "accent:light", "  --gl-accent: #ff0000;\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--gl-accent: #ff0000;") {
		t.Fatalf("generated content missing: %q", out)
	}
	if strings.Contains(out, "#000000") {
		t.Fatalf("old value should be replaced: %q", out)
	}
	if !strings.HasPrefix(out, "before\n") || !strings.HasSuffix(out, "after\n") {
		t.Fatalf("content outside the marked block should be untouched: %q", out)
	}
	// The markers themselves survive, so a second run stays idempotent.
	if !strings.Contains(out, "gentokens:accent:light:start") || !strings.Contains(out, "gentokens:accent:light:end") {
		t.Fatalf("markers should remain in place: %q", out)
	}
}

func TestReplaceMarkedMissingMarkerErrors(t *testing.T) {
	if _, err := replaceMarked("no markers here", "accent:light", "x"); err == nil {
		t.Fatal("expected an error when the marker is absent")
	}
}

func TestThemeLinesFormat(t *testing.T) {
	th := theme{Accent: "#111111", AccentBright: "#222222", AccentDeep: "#333333", AccentTint: "#44444444", OnAccent: "#555555"}
	got := th.lines("  ")
	want := "  --gl-accent: #111111;\n  --gl-accent-bright: #222222;\n  --gl-accent-deep: #333333;\n  --gl-accent-tint: #44444444;\n  --gl-on-accent: #555555;\n"
	if got != want {
		t.Fatalf("lines() = %q, want %q", got, want)
	}
}

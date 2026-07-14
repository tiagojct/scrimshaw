package server

import (
	"strings"
	"testing"
)

func TestTagChipClassIsDeterministicAndCaseInsensitive(t *testing.T) {
	if tagChipClass("News") != tagChipClass("news") {
		t.Fatal("tag color should not depend on case")
	}
	if tagChipClass(" tech ") != tagChipClass("tech") {
		t.Fatal("tag color should not depend on surrounding whitespace")
	}
	if tagChipClass("golang") != tagChipClass("golang") {
		t.Fatal("tag color should be stable across calls")
	}
}

func TestTagChipClassSpreadsAcrossThePalette(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n"} {
		class := tagChipClass(name)
		if !strings.HasPrefix(class, "tag-chip tag-c") {
			t.Fatalf("unexpected class shape: %q", class)
		}
		seen[strings.TrimPrefix(class, "tag-chip ")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected tag names to spread across more than one palette color, got %v", seen)
	}
}

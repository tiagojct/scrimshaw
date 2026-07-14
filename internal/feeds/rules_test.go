package feeds

import "testing"

func TestParseFeedRulesAndMatching(t *testing.T) {
	rules := parseFeedRules(`
# a comment, ignored
skip sponsored
tag:golang /\bgo\b/
tag: no name, ignored
garbage line with no verb
bogus-verb something
`)
	if len(rules) != 2 {
		t.Fatalf("parsed %d rules, want 2: %+v", len(rules), rules)
	}

	if !shouldSkip(rules, "This post is Sponsored by Acme") {
		t.Fatal("plain keyword match should be case-insensitive")
	}
	if shouldSkip(rules, "An ordinary post about gardening") {
		t.Fatal("unrelated text should not be skipped")
	}

	tags := matchingTags(rules, "Why Go is a great language")
	if len(tags) != 1 || tags[0] != "golang" {
		t.Fatalf("matchingTags = %v, want [golang]", tags)
	}
	if tags := matchingTags(rules, "Golfing is fun"); len(tags) != 0 {
		t.Fatalf("word-boundary regex should not match a substring: %v", tags)
	}
}

func TestCompileRulePatternKeywordIsLiteral(t *testing.T) {
	re, err := compileRulePattern("a.b")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("prefix a.b suffix") {
		t.Fatal("literal keyword should match itself")
	}
	if re.MatchString("prefix aXb suffix") {
		t.Fatal("a plain keyword's '.' must not act as a regex wildcard")
	}
}

func TestParseFeedRulesMalformedRegexIsSkippedNotFatal(t *testing.T) {
	rules := parseFeedRules("tag:x /[/\nskip fine")
	if len(rules) != 1 {
		t.Fatalf("expected the malformed regex rule to be dropped, kept %d: %+v", len(rules), rules)
	}
	if !shouldSkip(rules, "this is fine") {
		t.Fatal("the well-formed rule after the bad one should still work")
	}
}

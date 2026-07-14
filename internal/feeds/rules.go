package feeds

import (
	"regexp"
	"strings"
)

// feedRule is one line of a feed's content rules: either a skip rule
// (tag == "") that drops a matching entry before insert, or a tag rule that
// adds tag to a newly inserted item whose title+text matches pattern.
type feedRule struct {
	tag     string
	pattern *regexp.Regexp
}

// parseFeedRules parses one rule per line: "skip <pattern>" or
// "tag:<name> <pattern>". A pattern wrapped in /slashes/ is a regexp;
// otherwise a case-insensitive substring match. Blank lines and lines
// starting with # are ignored. A malformed line is skipped rather than
// failing the whole poll — see the 006 migration for the format.
func parseFeedRules(raw string) []feedRule {
	var rules []feedRule
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			continue
		}
		verb, rest := line[:sp], strings.TrimSpace(line[sp+1:])
		if rest == "" {
			continue
		}
		var tag string
		switch {
		case verb == "skip":
		case strings.HasPrefix(verb, "tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(verb, "tag:"))
			if tag == "" {
				continue
			}
		default:
			continue
		}
		re, err := compileRulePattern(rest)
		if err != nil {
			continue
		}
		rules = append(rules, feedRule{tag: tag, pattern: re})
	}
	return rules
}

// compileRulePattern turns a /regex/ into a case-insensitive Go regexp, or a
// plain keyword into a case-insensitive literal-substring regexp.
func compileRulePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) >= 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		return regexp.Compile("(?i)" + pattern[1:len(pattern)-1])
	}
	return regexp.Compile("(?i)" + regexp.QuoteMeta(pattern))
}

// shouldSkip reports whether any skip rule matches haystack.
func shouldSkip(rules []feedRule, haystack string) bool {
	for _, r := range rules {
		if r.tag == "" && r.pattern.MatchString(haystack) {
			return true
		}
	}
	return false
}

// matchingTags evaluates tag: rules against haystack, returning the tag
// names whose pattern matched (each name is returned at most once).
func matchingTags(rules []feedRule, haystack string) []string {
	var tags []string
	for _, r := range rules {
		if r.tag != "" && r.pattern.MatchString(haystack) {
			tags = append(tags, r.tag)
		}
	}
	return tags
}

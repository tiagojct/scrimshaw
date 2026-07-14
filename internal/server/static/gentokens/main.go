// Command gentokens rewrites the accent-color block in app.css from
// accent.json, the token's source of truth — the same JSON-source-of-truth
// pattern as the Try-Works and Armilar generators. It runs only via
// `go generate` (see the directive in static.go), never at build or request
// time, so app.css stays a fully static embedded asset with no runtime
// theming and no frontend build step.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// theme holds one color scheme's accent inputs. accentTint and the
// theme-invariant focus ring are handled separately: tint keeps its own
// value here since light and dark use different mix ratios, while the focus
// ring is derived in CSS from --gl-accent with color-mix(), so it never
// needs a field of its own or a dark-block redefinition.
type theme struct {
	Accent       string `json:"accent"`
	AccentBright string `json:"accentBright"`
	AccentDeep   string `json:"accentDeep"`
	AccentTint   string `json:"accentTint"`
	OnAccent     string `json:"onAccent"`
}

type tokens struct {
	Light theme `json:"light"`
	Dark  theme `json:"dark"`
}

func (t theme) lines(indent string) string {
	return fmt.Sprintf(
		"%[1]s--gl-accent: %[2]s;\n%[1]s--gl-accent-bright: %[3]s;\n%[1]s--gl-accent-deep: %[4]s;\n%[1]s--gl-accent-tint: %[5]s;\n%[1]s--gl-on-accent: %[6]s;\n",
		indent, t.Accent, t.AccentBright, t.AccentDeep, t.AccentTint, t.OnAccent,
	)
}

// replaceMarked substitutes the lines between a "gentokens:<name>:start" and
// "gentokens:<name>:end" comment (both left untouched) with newly generated
// lines. Spliced by capture-group index rather than through regexp's
// $-substitution, so generated content is never misread as
// backreference/escape syntax.
func replaceMarked(css, name, generated string) (string, error) {
	re := regexp.MustCompile(`(?s)([ \t]*/\* gentokens:` + name + `:start[^\n]*\*/\n).*?([ \t]*/\* gentokens:` + name + `:end \*/)`)
	loc := re.FindStringSubmatchIndex(css)
	if loc == nil {
		return "", fmt.Errorf("marker %q not found in app.css", name)
	}
	startMarkerLineEnd, endMarkerLineStart := loc[3], loc[4]
	return css[:startMarkerLineEnd] + generated + css[endMarkerLineStart:], nil
}

func main() {
	data, err := os.ReadFile("accent.json")
	if err != nil {
		fail(err)
	}
	var tk tokens
	if err := json.Unmarshal(data, &tk); err != nil {
		fail(err)
	}
	css, err := os.ReadFile("app.css")
	if err != nil {
		fail(err)
	}

	out, err := replaceMarked(string(css), "accent:light", tk.Light.lines("  "))
	if err != nil {
		fail(err)
	}
	out, err = replaceMarked(out, "accent:dark", tk.Dark.lines("    "))
	if err != nil {
		fail(err)
	}

	if err := os.WriteFile("app.css", []byte(out), 0644); err != nil {
		fail(err)
	}
	fmt.Println("gentokens: app.css accent block regenerated from accent.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gentokens:", err)
	os.Exit(1)
}

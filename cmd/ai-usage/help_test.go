package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// TestHelpLayout: the help fits the width it is given, and color changes
// none of its text.
func TestHelpLayout(t *testing.T) {
	for _, width := range []int{80, 100, 160} {
		plain := ansi.Strip(helpText(view.Options{Width: width}))
		colored := helpText(view.Options{Width: width, Color: true})
		if ansi.Strip(colored) != plain || colored == plain {
			t.Errorf("width %d: color changes the text, or adds none", width)
		}
		for l := range strings.SplitSeq(plain, "\n") {
			if ansi.StringWidth(l) > min(width, helpMax) {
				t.Errorf("width %d: %q is wider", width, l)
			}
		}
	}
}

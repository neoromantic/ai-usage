package view

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme is the report's semantic colors. The code names a token, never a
// raw color; each token has a value for dark and for light terminals.
type Theme struct {
	// Text is the terminal's own foreground.
	Text color.Color
	// Muted is for column headers, secondary text, and units.
	Muted color.Color
	// Faint is for tracks, rules, dots, and unknown bars.
	Faint color.Color
	// Accent is for keys, pills, and the this-device mark.
	Accent color.Color
	// The forecast states and their badges.
	Out, Over, Tight, OK, Under color.Color
	// Heat is the matrix ramp, from faint to bright. The top step is also
	// bold.
	Heat [5]color.Color
}

// NewTheme is the theme for a dark or a light background.
func NewTheme(dark bool) Theme {
	ld := lipgloss.LightDark(dark)
	c := func(light, dark string) color.Color { return ld(lipgloss.Color(light), lipgloss.Color(dark)) }
	return Theme{
		Text:   lipgloss.NoColor{},
		Muted:  c("#6c6c6c", "#8a8a8a"),
		Faint:  c("#b2b2b2", "#4e4e4e"),
		Accent: c("#0087af", "#5fafff"),
		Out:    c("#d70000", "#ff5f5f"),
		Over:   c("#d75f00", "#ff8700"),
		Tight:  c("#af8700", "#ffd75f"),
		OK:     c("#008700", "#87d75f"),
		Under:  c("#005fd7", "#5f87ff"),
		Heat: [5]color.Color{
			c("#bcbcbc", "#585858"),
			c("#949494", "#808080"),
			c("#6c6c6c", "#a8a8a8"),
			c("#3a3a3a", "#d0d0d0"),
			c("#000000", "#ffffff"),
		},
	}
}

// State is the color of a forecast state; unknown is faint.
func (t Theme) State(s string) color.Color {
	switch s {
	case StateOut:
		return t.Out
	case StateOver:
		return t.Over
	case StateTight:
		return t.Tight
	case StateOK:
		return t.OK
	case StateUnder:
		return t.Under
	default:
		return t.Faint
	}
}

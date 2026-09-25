package main

import (
	"image/color"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// helpIntro is the first line of the help.
const helpIntro = "`ai-usage` collects AI harness usage on this device and shares it with a team."

// helpSection is a heading of the help and its lines.
type helpSection struct {
	title string
	lines []helpLine
}

// helpLine is what to type and what it does. In what it does, `text` is
// typed as it is.
type helpLine struct{ what, does string }

// help is the help, in the order it is printed. A [SECTION] in a command
// is a section below, whose flags the command takes.
var help = []helpSection{
	{"REPORT", []helpLine{
		{"ai-usage [--json] [--offline] [VIEW] [DISPLAY]", "collect now and print the report"},
		{"ai-usage report [--json] [VIEW] [DISPLAY]", "print the last collected report, no collection"},
		{"ai-usage report --from FILE [VIEW] [DISPLAY]", "show a report saved with `--json`, such as a demo's, instead of this device's"},
		{"ai-usage status [--json] [DISPLAY]", "collector health: version, last success, last error"},
		{"ai-usage collect [--quiet] [--json] [--offline] [--home DIR] [VIEW] [DISPLAY]",
			"what the system scheduler runs; `--home` overrides `AI_USAGE_HOME`"},
	}},
	{"TEAM", []helpLine{
		{"ai-usage team", "show the team fingerprint and devices"},
		{"ai-usage team key", "print the team private key (the only secret)"},
		{"ai-usage team join [KEY]", "join a team; the key is read from stdin when omitted"},
		{"ai-usage team forget-device ID", "remove a device's snapshot from the relay"},
		{"ai-usage alias [ACCOUNT NAME | ACCOUNT --clear]",
			"list or set the short name the whole team sees for an account; ACCOUNT is a label, a name, or PROVIDER:LABEL"},
	}},
	{"THIS DEVICE", []helpLine{
		{"ai-usage name show|set NAME|clear", "what the team calls this device, instead of its host name"},
		{"ai-usage home", "list the harness homes this device reads"},
		{"ai-usage home add PROVIDER DIR... [--quota-from PROVIDER:DIR]",
			"read more homes; `--quota-from` names the Codex or Grok home whose login these Hermes homes bill through"},
		{"ai-usage home remove PROVIDER DIR... [--forget]",
			"stop reading homes added before; `--forget` also drops their sessions, for homes another collector reads now"},
	}},
	{"RELAY", []helpLine{
		{"ai-usage relay show|set URL|clear", "choose the relay this device publishes to"},
		{"ai-usage relay serve [--addr :8080] [--client-ip-header NAME]", "run a relay (Vercel KV from env, else memory)"},
	}},
	{"SCHEDULE", []helpLine{
		{"ai-usage schedule install", "register with the system scheduler, and let later runs keep it registered"},
		{"ai-usage schedule remove", "unregister, and stop later runs from registering again"},
		{"ai-usage schedule status", "whether the scheduler runs this binary with this state folder"},
		{"ai-usage schedule run",
			"collect every 15 minutes in the foreground, where there is no system scheduler, as in a container"},
	}},
	{"RELEASES", []helpLine{
		{"ai-usage update", "check for a release now"},
		{"ai-usage version", "print this binary's version"},
	}},
	{"VIEW", []helpLine{
		{"--projects", "every project on this device, not only the busiest"},
		{"--devices", "DEVICES as each device's status; `s` switches views in the interactive view"},
	}},
	{"DISPLAY", []helpLine{
		{"--color=auto|always|never", "auto colors a terminal, unless `NO_COLOR` is set or `TERM=dumb`"},
		{"--ascii", "ASCII glyphs; the default without a UTF-8 locale"},
		{"--width N", "columns, 80 to 160; default: the terminal's, else `COLUMNS`, else 80"},
		{"--plain", "print the report; on a terminal, the default is the interactive view, with every key under `?`"},
	}},
	{"ENVIRONMENT", []helpLine{
		{"AI_USAGE_HOME", "collector directory (default: OS config dir/ai-usage)"},
		{"AI_USAGE_RELAY", "relay URL, overrides the configured one"},
		{"AI_USAGE_NAME", "this device's name in the team, over the configured one, in runs that see it"},
		{"AI_USAGE_NO_SCHEDULE", "set to skip scheduler registration on this run"},
		{"CLAUDE_CONFIG_DIR", "another data folder for Claude Code"},
		{"CODEX_HOME", "another data folder for Codex"},
		{"GROK_HOME", "another data folder for Grok"},
		{"HERMES_HOME", "another data folder for Hermes"},
	}},
}

// writeHelp writes the help to w as the static report is written: in color
// on a terminal that takes it, and wrapped to the terminal's width.
func writeHelp(w io.Writer) {
	d := &display{color: "auto"}
	io.WriteString(d.writer(w), helpText(d.options(w, true)))
}

// The help's lines are indented. What a line does starts at a column right
// of the widest thing to type that leaves it half the width, one column for
// the commands and one for the flags and variables; a wider one leaves what
// it does for the next line. The help is at least helpMin wide and at most
// helpMax, to be read easily.
const (
	helpIndent = 2
	helpGap    = 3
	helpMin    = 60
	helpMax    = 120
)

// helpText is the help, width wide within helpMin and helpMax.
func helpText(o view.Options) string {
	width := min(max(o.Width, helpMin), helpMax)
	pt := newHelpPaint(o)
	cols := map[bool]int{}
	for _, s := range help {
		for _, l := range s.lines {
			if w := ansi.StringWidth(l.what); w <= width/2-helpIndent-helpGap {
				cmd := isCommand(l.what)
				cols[cmd] = max(cols[cmd], w+helpIndent+helpGap)
			}
		}
	}

	var b strings.Builder
	b.WriteString(strings.Join(pt.wrap(helpIntro, width), "\n") + "\n")
	for _, s := range help {
		b.WriteString("\n" + pt.style(s.title, toneHeading) + "\n")
		for _, l := range s.lines {
			col := cols[isCommand(l.what)]
			what := pt.syntax(l.what)
			ww := helpIndent + ansi.StringWidth(l.what)
			does := pt.wrap(l.does, width-col)
			b.WriteString(strings.Repeat(" ", helpIndent) + what)
			if ww+helpGap > col {
				b.WriteString("\n" + strings.Repeat(" ", col))
			} else {
				b.WriteString(strings.Repeat(" ", col-ww))
			}
			b.WriteString(strings.Join(does, "\n"+strings.Repeat(" ", col)) + "\n")
		}
	}
	return b.String()
}

// isCommand is whether what to type is a command, not a flag or a variable.
func isCommand(what string) bool { return strings.HasPrefix(what, "ai-usage") }

// tone is what a piece of the help is, for its style.
type tone int

const (
	tonePlain   tone = iota
	toneHeading      // a section heading
	toneProgram      // ai-usage, before each command
	toneCommand      // a command, typed as it is
	toneLiteral      // a flag, a value, or a variable, typed as it is
	toneArg          // a placeholder, such as DIR, to fill in
	toneSection      // a placeholder for the flags of a section
	tonePunct        // brackets, bars, commas, and ellipses
)

// helpPaint styles the help's text with the report's theme. Without color,
// headings and commands are still bold.
type helpPaint struct {
	color bool
	th    view.Theme
}

func newHelpPaint(o view.Options) helpPaint {
	return helpPaint{color: o.Color, th: view.NewTheme(o.Dark)}
}

func (p helpPaint) style(s string, t tone) string {
	st := lipgloss.NewStyle()
	if !p.color {
		if t == toneHeading || t == toneCommand {
			return st.Bold(true).Render(s)
		}
		return s
	}
	fg := func(c color.Color) lipgloss.Style { return st.Foreground(c) }
	switch t {
	case toneHeading:
		st = fg(p.th.Tight).Bold(true)
	case toneProgram:
		st = fg(p.th.Muted)
	case toneCommand:
		st = fg(p.th.Accent).Bold(true)
	case toneLiteral:
		st = fg(p.th.Accent)
	case toneArg:
		st = fg(p.th.OK)
	case toneSection:
		st = fg(p.th.Tight)
	case tonePunct:
		st = fg(p.th.Faint)
	default:
		return s
	}
	return st.Render(s)
}

// syntax styles what to type: the program, its commands, flags and their
// values, placeholders in capitals, and the brackets and bars between them.
// A capital name with an underscore is a variable, typed as it is.
func (p helpPaint) syntax(s string) string {
	var b strings.Builder
	flag := false
	for i, w := range syntaxWords(s) {
		t := tonePlain
		switch {
		case w == " ":
			flag = false
		case strings.Trim(w, helpPunct) == "":
			t = tonePunct
		case i == 0 && w == "ai-usage":
			t = toneProgram
		// A flag's value, as in --color=auto, is typed as the flag is.
		case flag, strings.HasPrefix(w, "-"), strings.HasPrefix(w, ":"), strings.Contains(w, "_"):
			t, flag = toneLiteral, flag || strings.HasPrefix(w, "-")
		case helpSectionNamed(w):
			t = toneSection
		case strings.ToUpper(w) == w:
			t = toneArg
		default:
			t = toneCommand
		}
		b.WriteString(p.style(w, t))
	}
	return b.String()
}

// helpSectionNamed is whether a section of the help is titled s.
func helpSectionNamed(s string) bool {
	for _, sec := range help {
		if sec.title == s {
			return true
		}
	}
	return false
}

// helpPunct is the punctuation of what to type.
const helpPunct = "[]|,=."

// syntaxWords splits s into words, spaces, and runs of punctuation.
func syntaxWords(s string) []string {
	kind := func(r rune) int {
		switch {
		case r == ' ':
			return 0
		case strings.ContainsRune(helpPunct, r):
			return 1
		}
		return 2
	}
	var out []string
	start, prev := 0, ' '
	for i, r := range s {
		if i > start && (r == ' ' || kind(r) != kind(prev)) {
			out = append(out, s[start:i])
			start = i
		}
		prev = r
	}
	return append(out, s[start:])
}

// wrap styles text, where `text` is typed as it is, and breaks it into
// lines of at most width columns between words.
func (p helpPaint) wrap(text string, width int) []string {
	var lines []string
	var line strings.Builder
	n := 0
	code := false
	for _, w := range strings.Fields(text) {
		ww := 0
		var styled strings.Builder
		for i, part := range strings.Split(w, "`") {
			if i > 0 {
				code = !code
			}
			if part == "" {
				continue
			}
			ww += ansi.StringWidth(part)
			if code {
				styled.WriteString(p.code(part))
			} else {
				styled.WriteString(part)
			}
		}
		if n > 0 && n+1+ww > width {
			lines = append(lines, line.String())
			line.Reset()
			n = 0
		}
		if n > 0 {
			line.WriteString(" ")
			n++
		}
		line.WriteString(styled.String())
		n += ww
	}
	return append(lines, line.String())
}

// code styles text typed as it is: the program as a command, anything else
// as a flag or a variable.
func (p helpPaint) code(s string) string {
	if s == "ai-usage" {
		return p.style(s, toneCommand)
	}
	return p.style(s, toneLiteral)
}

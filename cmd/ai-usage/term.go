package main

import (
	"flag"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"

	"github.com/neoromantic/ai-usage/internal/view"
)

// display is the console flags of collect, report, and status.
type display struct {
	color    string
	ascii    bool
	width    int
	projects bool
	// plain prints the static report on a terminal too, rather than open
	// the interactive view.
	plain bool
}

// displayFlags adds the console flags to fs, and the view choices with views.
func displayFlags(fs *flag.FlagSet, views bool) *display {
	d := &display{}
	fs.StringVar(&d.color, "color", "auto", "")
	fs.BoolVar(&d.ascii, "ascii", false, "")
	fs.IntVar(&d.width, "width", 0, "")
	fs.BoolVar(&d.plain, "plain", false, "")
	if views {
		fs.BoolVar(&d.projects, "projects", false, "")
	}
	return d
}

// check rejects bad display flags before a collection starts.
func (d *display) check() error {
	switch d.color {
	case "auto", "always", "never":
	default:
		return usageError("--color takes auto, always, or never")
	}
	if d.width < 0 {
		return usageError("--width takes a number of columns")
	}
	return nil
}

// options is how the report is drawn on stdout: in color when the
// terminal takes color, for its background when it can be asked.
func (d *display) options(stdout io.Writer) view.Options {
	o := view.Options{
		Width:       termWidth(d.width, stdout),
		Color:       d.profile(stdout) >= colorprofile.ANSI,
		Dark:        true,
		ASCII:       d.ascii || !utf8Locale(),
		AllProjects: d.projects,
	}
	if o.Color {
		o.Dark = darkBackground(stdout)
	}
	return o
}

// writer is stdout through a writer that keeps only the escapes the
// terminal takes.
func (d *display) writer(stdout io.Writer) io.Writer {
	return &colorprofile.Writer{Forward: stdout, Profile: d.profile(stdout)}
}

// profile is what stdout takes. A pipe, TERM=dumb, and --color=never take
// no escapes; NO_COLOR takes bold and reverse video but no color; and
// --color=always takes color anywhere, NO_COLOR or not.
func (d *display) profile(stdout io.Writer) colorprofile.Profile {
	env := os.Environ()
	var p colorprofile.Profile
	switch d.color {
	case "never":
		p = colorprofile.NoTTY
	case "always":
		asked := env[:0:0]
		for _, kv := range env {
			if !strings.HasPrefix(kv, "NO_COLOR=") {
				asked = append(asked, kv)
			}
		}
		p = max(colorprofile.Env(asked), colorprofile.ANSI)
	default:
		p = colorprofile.Detect(stdout, env)
	}
	if p > colorprofile.NoTTY && !enableVT(stdout) {
		p = colorprofile.NoTTY
	}
	return p
}

// darkBackground asks the terminal for its background when both ends are
// a terminal; otherwise it is taken to be dark.
func darkBackground(stdout io.Writer) bool {
	out, ok := stdout.(*os.File)
	if !ok {
		return true
	}
	if _, tty := terminal(out); !tty {
		return true
	}
	if _, tty := terminal(os.Stdin); !tty {
		return true
	}
	return lipgloss.HasDarkBackground(os.Stdin, out)
}

// terminal is the descriptor of w when it is a terminal.
func terminal(w io.Writer) (int, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
}

// termWidth is the flag, else the terminal's width, else $COLUMNS, else 80.
// The renderer clamps it.
func termWidth(flagWidth int, stdout io.Writer) int {
	if flagWidth > 0 {
		return flagWidth
	}
	if fd, ok := terminal(stdout); ok {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && n > 0 {
		return n
	}
	return 80
}

// utf8Locale is whether the terminal can be trusted with UTF-8 glyphs. On
// Windows, Windows Terminal and editor terminals set no locale but can.
func utf8Locale() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToLower(os.Getenv(k)); v != "" {
			if strings.Contains(v, "utf-8") || strings.Contains(v, "utf8") {
				return true
			}
			break
		}
	}
	return runtime.GOOS == "windows" && (os.Getenv("WT_SESSION") != "" || os.Getenv("TERM_PROGRAM") != "")
}

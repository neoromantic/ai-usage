package main

import (
	"flag"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

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

// options is how the report is drawn on stdout.
func (d *display) options(stdout io.Writer) view.Options {
	return view.Options{
		Width:       termWidth(d.width, stdout),
		Color:       useColor(d.color, stdout),
		Dark:        true,
		ASCII:       d.ascii || !utf8Locale(),
		AllProjects: d.projects,
	}
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

func useColor(mode string, stdout io.Writer) bool {
	switch mode {
	case "always":
		return enableVT(stdout)
	case "never":
		return false
	}
	_, tty := terminal(stdout)
	return tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && enableVT(stdout)
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

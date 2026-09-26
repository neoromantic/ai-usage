package main

import (
	"flag"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

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
	// devices draws DEVICES as its status view, and opens the interactive
	// view there.
	devices bool
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
		fs.BoolVar(&d.devices, "devices", false, "")
	}
	return d
}

// parse reads a drawing command's flags and rejects bad display flags
// before a collection starts.
func (d *display) parse(fs *flag.FlagSet, args []string) error {
	if err := parse(fs, args); err != nil {
		return err
	}
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
// terminal takes color, for its background when it is known. ask lets the
// terminal be asked, as the static report does.
func (d *display) options(stdout io.Writer, ask bool) view.Options {
	o := view.Options{
		Width:        termWidth(d.width, stdout),
		Color:        d.profile(stdout) >= colorprofile.ANSI,
		Dark:         true,
		ASCII:        d.ascii || !utf8Locale(),
		AllProjects:  d.projects,
		DeviceStatus: d.devices,
	}
	if o.Color {
		o.Dark = darkBackground(stdout, ask)
	}
	return o
}

// writer is stdout through a writer that keeps only the escapes the
// terminal takes.
func (d *display) writer(stdout io.Writer) io.Writer {
	return &colorprofile.Writer{Forward: stdout, Profile: d.profile(stdout)}
}

// profile is what stdout takes. A pipe, TERM=dumb, and --color=never take
// no escapes; NO_COLOR, set to anything, takes bold and reverse video but no
// color; and --color=always takes color anywhere, NO_COLOR or not.
func (d *display) profile(stdout io.Writer) colorprofile.Profile {
	// colorprofile reads NO_COLOR as a boolean, so any value is passed on as 1.
	var env []string
	for _, kv := range os.Environ() {
		if v, ok := strings.CutPrefix(kv, "NO_COLOR="); ok {
			if v == "" || d.color == "always" {
				continue
			}
			kv = "NO_COLOR=1"
		}
		env = append(env, kv)
	}
	var p colorprofile.Profile
	switch d.color {
	case "never":
		p = colorprofile.NoTTY
	case "always":
		p = max(colorprofile.Env(env), colorprofile.ANSI)
	default:
		p = colorprofile.Detect(stdout, env)
	}
	if p > colorprofile.NoTTY && !enableVT(stdout) {
		p = colorprofile.NoTTY
	}
	return p
}

// background asks the terminal stdout is drawn on what its background is,
// and says whether it answered. Tests replace it.
var background = askBackground

// darkBackground is whether the terminal's background is dark: as COLORFGBG
// says, else as the terminal answers when ask lets it be asked, else dark.
func darkBackground(stdout io.Writer, ask bool) bool {
	if dark, ok := fgbgDark(os.Getenv("COLORFGBG")); ok {
		return dark
	}
	if ask {
		if dark, ok := background(stdout); ok {
			return dark
		}
	}
	return true
}

// fgbgDark reads COLORFGBG, "fg;bg" or "fg;default;bg", as rxvt and others
// set it: a background of color 0 to 6, or 8, is dark, as Vim takes it.
func fgbgDark(v string) (dark, ok bool) {
	i := strings.LastIndexByte(v, ';')
	bg, err := strconv.Atoi(v[i+1:])
	if i < 0 || err != nil || bg < 0 || bg > 15 {
		return false, false
	}
	return bg <= 6 || bg == 8, true
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

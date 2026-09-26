package schedule

import (
	"context"
	"slices"
	"strings"
)

// maxLine keeps the crontab entry inside the 1000-byte command buffer of
// Vixie-derived crons (macOS, Debian), which cut a longer command silently.
const maxLine = 990

// cronPath is cron's own PATH. It is kept when a long PATH is shortened, so
// the collector can still find crontab and the tools harness scripts call.
var cronPath = []string{"/usr/bin", "/bin"}

// Line is the crontab entry. PATH is captured from the installing shell,
// because cron's own PATH does not reach the harness binaries. A PATH too
// long for cron keeps its leading directories, where shells put the person's
// own tools, plus cron's default directories.
// A % in the command would end it in crontab syntax, so it is escaped.
func Line(exe, home, path string) string {
	dirs := pathDirs(path)
	if line := cronLine(exe, home, dirs); len(line) <= maxLine {
		return line
	}
	withRequired := func(dirs []string) []string {
		out := append([]string(nil), dirs...)
		for _, d := range cronPath {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
		return out
	}
	var kept []string
	full := false
	for _, d := range dirs {
		switch {
		case slices.Contains(cronPath, d):
			kept = append(kept, d)
		case full:
		case len(cronLine(exe, home, withRequired(append(slices.Clip(kept), d)))) > maxLine:
			// Stop at the first directory that does not fit, so no later
			// directory takes over a lookup an earlier one would have won.
			full = true
		default:
			kept = append(kept, d)
		}
	}
	return cronLine(exe, home, withRequired(kept))
}

func cronLine(exe, home string, dirs []string) string {
	cmd := shCommand(exe, home) + " >/dev/null 2>&1"
	if len(dirs) > 0 {
		cmd = "PATH=" + shq(strings.Join(dirs, ":")) + " " + cmd
	}
	return "*/15 * * * * " + cronEscape(cmd) + " # " + Marker
}

// shCommand is the binary and its arguments, quoted for sh.
func shCommand(exe, home string) string { return shq(exe) + " " + args(home, shq) }

// args are the collector's arguments in a scheduled run. The state folder is
// named because the scheduler does not see AI_USAGE_HOME or XDG_CONFIG_HOME,
// and another folder would mean another device, team key, and ledger.
func args(home string, quote func(string) string) string {
	return "collect --quiet --home " + quote(home)
}

// pathDirs drops empty and relative PATH entries, which would resolve against
// cron's working directory, and repeats, which never win a lookup.
func pathDirs(path string) []string {
	var out []string
	for d := range strings.SplitSeq(path, ":") {
		if !strings.HasPrefix(d, "/") || slices.Contains(out, d) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (s Scheduler) installCron(ctx context.Context, exe, home, path string) error {
	lines, err := s.cronLines(ctx)
	if err != nil {
		return err
	}
	kept := withoutMarker(lines)
	kept = append(kept, Line(exe, home, path))
	return s.writeCron(ctx, kept)
}

func (s Scheduler) removeCron(ctx context.Context) error {
	lines, err := s.cronLines(ctx)
	if err != nil {
		return err
	}
	kept := withoutMarker(lines)
	if len(kept) == len(lines) {
		return nil
	}
	return s.writeCron(ctx, kept)
}

func (s Scheduler) lookupCron(ctx context.Context, exe, home string) (State, error) {
	lines, err := s.cronLines(ctx)
	if err != nil {
		return Absent, err
	}
	run := " " + cronEscape(shCommand(exe, home)) + " "
	got := Absent
	for _, l := range lines {
		switch {
		case !isOurs(l):
		case commented(l):
			if got == Absent && strings.Contains(l, "' collect ") {
				got = Disabled
			}
		case strings.Contains(l, run):
			return Active, nil
		default:
			got = Other
		}
	}
	return got, nil
}

// cronLines reads the crontab. Only the scheduler's own "no crontab" answer
// means empty: any other failure is returned, so a later write cannot replace
// a crontab that was never read.
func (s Scheduler) cronLines(ctx context.Context) ([]string, error) {
	out, err := s.Run(ctx, "crontab", []string{"-l"}, nil)
	if err != nil {
		if noCrontab(err.Error()) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// noCrontab recognizes an empty crontab: "no crontab for user" from Vixie
// cron, cronie, and macOS, and a missing spool file from BusyBox.
func noCrontab(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "no crontab for") ||
		(strings.Contains(msg, "can't open") && strings.Contains(msg, "no such file or directory"))
}

func (s Scheduler) writeCron(ctx context.Context, lines []string) error {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	_, err := s.Run(ctx, "crontab", []string{"-"}, []byte(body))
	return err
}

// isOurs recognizes the collector's entry, active or commented out, so
// Install and Remove replace both.
func isOurs(line string) bool {
	return strings.HasSuffix(strings.TrimSpace(line), "# "+Marker)
}

func commented(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func withoutMarker(lines []string) []string {
	out := []string{}
	for _, l := range lines {
		if !isOurs(l) {
			out = append(out, l)
		}
	}
	return out
}

// shq quotes s for sh.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cronEscape keeps cron from reading % as the end of the command.
func cronEscape(s string) string { return strings.ReplaceAll(s, "%", `\%`) }

package schedule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeCron is a crontab kept in memory. A nil tab means the user has none.
type fakeCron struct {
	tab     *string
	readErr error
	calls   []string
}

func (f *fakeCron) run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch {
	case name == "crontab" && reflect.DeepEqual(args, []string{"-l"}):
		if f.readErr != nil {
			return nil, f.readErr
		}
		if f.tab == nil {
			return nil, errors.New("crontab: no crontab for tester")
		}
		return []byte(*f.tab), nil
	case name == "crontab" && reflect.DeepEqual(args, []string{"-"}):
		s := string(stdin)
		f.tab = &s
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}

func (f *fakeCron) writes() int {
	n := 0
	for _, c := range f.calls {
		if c == "crontab -" {
			n++
		}
	}
	return n
}

func withTab(s string) *fakeCron { return &fakeCron{tab: &s} }

func cron(f *fakeCron) Scheduler { return Scheduler{GOOS: "linux", Run: f.run} }

const (
	exe  = "/opt/ai-usage/bin/ai-usage"
	home = "/data/ai-usage"
)

func TestLineFormat(t *testing.T) {
	// Every 15 minutes, with the installing shell's PATH, marked as ours.
	got := Line(exe, home, "/usr/local/bin:/usr/bin:/bin")
	if !strings.HasPrefix(got, "*/15 * * * * ") || !strings.HasSuffix(got, " # "+Marker) ||
		!strings.Contains(got, "PATH='/usr/local/bin:/usr/bin:/bin' ") {
		t.Fatalf("Line = %s", got)
	}
	// With no usable PATH, cron's default is left alone rather than emptied.
	if got := Line(exe, home, ":.:bin"); strings.Contains(got, "PATH=") {
		t.Fatalf("Line with no absolute PATH entries sets PATH: %s", got)
	}
	if got := Line(exe, home, "/a:/b:/a::./x"); !strings.Contains(got, "PATH='/a:/b' ") {
		t.Fatalf("Line keeps repeats or relative entries: %s", got)
	}
}

// TestLineRunsUnderSh hands the entry to sh the way cron does and checks the
// binary sees the PATH and arguments, with spaces, quotes, and percents intact.
func TestLineRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cron entries run under sh")
	}
	dir := filepath.Join(t.TempDir(), "it's 100% ai usage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "ai-usage")
	state := filepath.Join(dir, "state 'x' 100%")
	script := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$PATH\" \"$*\" > \"$AIU_OUT\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	long := longPath(80)
	for _, tc := range []struct{ name, path, want string }{
		{"quoted", "/opt/it's 50%/bin:/usr/bin:/bin", "/opt/it's 50%/bin:/usr/bin:/bin"},
		{"trimmed", long, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Line(bin, state, tc.path)
			out := filepath.Join(t.TempDir(), "out")
			cmd := exec.Command("/bin/sh", "-c", cronCommand(t, line))
			cmd.Env = []string{"AIU_OUT=" + out}
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("sh: %v %s\nline: %s", err, b, line)
			}
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			if len(got) != 2 || got[1] != "collect --quiet --home "+state {
				t.Fatalf("binary saw %q", got)
			}
			if tc.want != "" && got[0] != tc.want {
				t.Fatalf("PATH = %q, want %q", got[0], tc.want)
			}
			if tc.want == "" && !strings.HasPrefix(tc.path, got[0][:len(got[0])-len(":/usr/bin:/bin")]) {
				t.Fatalf("trimmed PATH %q is not a prefix of the original", got[0])
			}
		})
	}
}

// cronCommand is what Vixie cron hands to sh: the text after the five time
// fields with \% unescaped. A bare % would end the command.
func cronCommand(t *testing.T, line string) string {
	t.Helper()
	f := strings.SplitN(line, " ", 6)
	if len(f) != 6 {
		t.Fatalf("not a crontab entry: %s", line)
	}
	var b strings.Builder
	escaped := false
	for _, r := range f[5] {
		switch {
		case escaped:
			escaped = false
			if r != '%' {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		case r == '\\':
			escaped = true
		case r == '%':
			t.Fatalf("bare %% ends the command early: %s", line)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// longPath is an interactive-shell PATH: tool directories first, repeats,
// a relative entry, and the system directories last.
func longPath(n int) string {
	var dirs []string
	for i := range n {
		dirs = append(dirs, fmt.Sprintf("/Users/someone/.local/share/tools/pkg-%02d/bin", i))
		if i == n/2 {
			dirs = append(dirs, ".", "/usr/bin", dirs[0])
		}
	}
	return strings.Join(append(dirs, "/usr/sbin", "/bin", "/sbin"), ":")
}

func TestLineFitsCronLimit(t *testing.T) {
	path := longPath(80)
	line := Line(exe, home, path)
	if len(line) > maxLine {
		t.Fatalf("line is %d bytes, limit %d", len(line), maxLine)
	}
	start := strings.Index(line, "PATH='") + len("PATH='")
	end := strings.Index(line[start:], "'") + start
	got := strings.Split(line[start:end], ":")
	if !slices.Contains(got, "/usr/bin") || !slices.Contains(got, "/bin") {
		t.Fatalf("trimmed PATH lost cron's defaults: %v", got)
	}
	// The kept directories are the leading ones, in their order.
	own := func(ds []string) (out []string) {
		for _, d := range ds {
			if !slices.Contains(cronPath, d) {
				out = append(out, d)
			}
		}
		return out
	}
	kept, all := own(got), own(pathDirs(path))
	if len(kept) < 10 || !reflect.DeepEqual(kept, all[:len(kept)]) {
		t.Fatalf("trimmed PATH is not a prefix of the original: %v", got)
	}
	if !strings.HasSuffix(line, " '"+exe+"' collect --quiet --home '"+home+"' >/dev/null 2>&1 # ai-usage") {
		t.Fatalf("command was cut: %s", line)
	}
}

func TestInstallIntoEmptyCrontab(t *testing.T) {
	f := &fakeCron{}
	s := cron(f)
	if err := s.Install(context.Background(), exe, home, "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}
	if want := Line(exe, home, "/usr/bin:/bin") + "\n"; f.tab == nil || *f.tab != want {
		t.Fatalf("crontab = %v, want %q", f.tab, want)
	}
	if got, err := s.Lookup(context.Background(), exe, home); err != nil || got != Active {
		t.Fatalf("Lookup after Install = %v, %v", got, err)
	}
}

func TestInstallKeepsOtherLines(t *testing.T) {
	existing := "# nightly\nMAILTO=me@example.com\n0 3 * * * /usr/local/bin/backup --all\n\n" +
		"*/15 * * * * PATH='/old' '/old/ai-usage' collect --quiet >/dev/null 2>&1 # ai-usage\n" +
		"30 * * * * echo 50\\% done\n"
	f := withTab(existing)
	s := cron(f)
	ctx := context.Background()
	if err := s.Install(ctx, exe, home, "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}
	want := "# nightly\nMAILTO=me@example.com\n0 3 * * * /usr/local/bin/backup --all\n\n" +
		"30 * * * * echo 50\\% done\n" + Line(exe, home, "/usr/bin:/bin") + "\n"
	if *f.tab != want {
		t.Fatalf("crontab:\n%s\nwant:\n%s", *f.tab, want)
	}
	// Installing again changes nothing.
	if err := s.Install(ctx, exe, home, "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}
	if *f.tab != want {
		t.Fatalf("second Install changed the crontab:\n%s", *f.tab)
	}
	if got, _ := s.Lookup(ctx, "/old/ai-usage", ""); got != Other {
		t.Fatalf("old entry: Lookup = %v", got)
	}
}

func TestRemove(t *testing.T) {
	ours := Line(exe, home, "/usr/bin:/bin")
	for _, tc := range []struct {
		name   string
		f      *fakeCron
		want   string
		writes int
	}{
		{"keeps others", withTab("MAILTO=x\n" + ours + "\n0 * * * * true\n"), "MAILTO=x\n0 * * * * true\n", 1},
		{"only ours", withTab(ours + "\n"), "", 1},
		{"commented out", withTab("# " + ours + "\n0 * * * * true\n"), "0 * * * * true\n", 1},
		{"absent", withTab("0 * * * * true\n"), "0 * * * * true\n", 0},
		{"no crontab", &fakeCron{}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := cron(tc.f).Remove(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.f.writes() != tc.writes {
				t.Fatalf("wrote the crontab %d times, want %d", tc.f.writes(), tc.writes)
			}
			got := ""
			if tc.f.tab != nil {
				got = *tc.f.tab
			}
			if got != tc.want {
				t.Fatalf("crontab = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	tab := "0 * * * * '/opt/ai-usage/bin/ai-usage' collect\n" + // ours without the marker
		"*/15 * * * * PATH='/elsewhere/ai-usage' '/opt/ai-usage/bin/ai-usage-old' collect --quiet --home '/data/ai-usage' >/dev/null 2>&1 # ai-usage\n"
	ctx := context.Background()
	for _, tc := range []struct {
		name, exe, home string
		want            State
	}{
		{"other binary", exe, home, Other},
		{"that binary", "/opt/ai-usage/bin/ai-usage-old", home, Active},
		{"that binary, other state folder", "/opt/ai-usage/bin/ai-usage-old", "/data/other", Other},
		{"PATH is not the binary", "/elsewhere/ai-usage", home, Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cron(withTab(tab)).Lookup(ctx, tc.exe, tc.home)
			if err != nil || got != tc.want {
				t.Fatalf("Lookup(%q, %q) = %v, %v; want %v", tc.exe, tc.home, got, err, tc.want)
			}
		})
	}
	if got, err := cron(&fakeCron{}).Lookup(ctx, exe, home); got != Absent || err != nil {
		t.Fatalf("Lookup with no crontab = %v, %v", got, err)
	}
	odd, oddHome := "/Users/o'brien/100% ai/ai-usage", "/Users/o'brien/100% state"
	f := &fakeCron{}
	if err := cron(f).Install(ctx, odd, oddHome, "/bin"); err != nil {
		t.Fatal(err)
	}
	if got, _ := cron(f).Lookup(ctx, odd, oddHome); got != Active {
		t.Fatalf("a quoted, escaped path is not found again:\n%s", *f.tab)
	}
}

// A person pauses the job by commenting its line out. That is not running,
// and a run must not quietly turn it back on.
func TestLookupCommentedOut(t *testing.T) {
	ctx := context.Background()
	ours := Line(exe, home, "/usr/bin:/bin")
	for _, tc := range []struct {
		name, tab string
		want      State
	}{
		{"commented", "#" + ours + "\n", Disabled},
		{"commented with space", "  # " + ours + "\n", Disabled},
		{"commented, another binary", "# " + Line("/old/ai-usage", home, "/bin") + "\n", Disabled},
		{"a remark that is not the job", "# ai-usage\n", Absent},
		{"commented and active", "# " + Line("/old/ai-usage", home, "/bin") + "\n" + ours + "\n", Active},
		{"commented and another active", "# " + ours + "\n" + Line("/old/ai-usage", home, "/bin") + "\n", Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := cron(withTab(tc.tab)).Lookup(ctx, exe, home); err != nil || got != tc.want {
				t.Fatalf("Lookup = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	// Install turns it back on.
	f := withTab("# " + ours + "\n")
	if err := cron(f).Install(ctx, exe, home, "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}
	if *f.tab != ours+"\n" {
		t.Fatalf("crontab after Install = %q", *f.tab)
	}
}

// A crontab that could not be read must never be written: the write would
// replace every other entry the person has.
func TestReadFailureNeverWrites(t *testing.T) {
	silent := silentExit(t)
	var exitErr *exec.ExitError
	if !errors.As(silent, &exitErr) {
		t.Fatalf("helper did not produce an exit error: %v", silent)
	}
	for _, readErr := range []error{
		errors.New("crontab: Operation not permitted"),
		silent,
	} {
		t.Run(readErr.Error(), func(t *testing.T) {
			f := withTab("0 3 * * * /usr/local/bin/backup\n")
			f.readErr = readErr
			s := cron(f)
			ctx := context.Background()
			if err := s.Install(ctx, exe, home, "/bin"); err == nil {
				t.Fatal("Install succeeded without reading the crontab")
			}
			if err := s.Remove(ctx); err == nil {
				t.Fatal("Remove succeeded without reading the crontab")
			}
			if _, err := s.Lookup(ctx, exe, home); err == nil {
				t.Fatal("Lookup hid the read error")
			}
			if f.writes() != 0 {
				t.Fatalf("crontab was written after a failed read: %v", f.calls)
			}
		})
	}
}

func TestNoCrontab(t *testing.T) {
	for msg, want := range map[string]bool{
		"crontab: no crontab for alice":                          true, // Vixie, cronie, macOS
		"crontab: can't open 'alice': No such file or directory": true, // BusyBox
		"crontab: Operation not permitted":                       false,
		"crontab: can't open 'alice': Permission denied":         false,
		"crontab: /var/spool/cron: No such file or directory":    false,
	} {
		if got := noCrontab(msg); got != want {
			t.Errorf("noCrontab(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestInstallRejectsLineBreaks(t *testing.T) {
	f := withTab("0 * * * * true\n")
	if err := cron(f).Install(context.Background(), exe, home, "/bin\n* * * * * rm -rf ~"); err == nil {
		t.Fatal("PATH with a line break was scheduled")
	}
	if err := cron(f).Install(context.Background(), exe, "/data\n* * * * * rm -rf ~", "/bin"); err == nil {
		t.Fatal("a state folder with a line break was scheduled")
	}
	if f.writes() != 0 {
		t.Fatal("crontab was written")
	}
}

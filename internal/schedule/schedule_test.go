package schedule

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
	got := Line(exe, home, "/usr/local/bin:/usr/bin:/bin")
	want := `*/15 * * * * PATH='/usr/local/bin:/usr/bin:/bin' '/opt/ai-usage/bin/ai-usage' collect --quiet --home '/data/ai-usage' >/dev/null 2>&1 # ai-usage`
	if got != want {
		t.Fatalf("Line:\n got %s\nwant %s", got, want)
	}
	// With no usable PATH, cron's default is left alone rather than emptied.
	if got := Line(exe, home, ":.:bin"); strings.Contains(got, "PATH=") {
		t.Fatalf("Line with no absolute PATH entries sets PATH: %s", got)
	}
	if got := Line(exe, home, "/a:/b:/a::./x"); !strings.Contains(got, "PATH='/a:/b' ") {
		t.Fatalf("Line keeps repeats or relative entries: %s", got)
	}
}

func TestLineEscaping(t *testing.T) {
	odd := "/Users/o'brien/AI Tools 100%/ai-usage"
	line := Line(odd, "/Users/o'brien/state 100%", "/opt/it's 50%/bin")
	if strings.Count(line, "%") != strings.Count(line, `\%`) {
		t.Fatalf("unescaped %% in %s", line)
	}
	if !strings.Contains(line, `'/Users/o'\''brien/AI Tools 100\%/ai-usage' collect --quiet --home '/Users/o'\''brien/state 100\%'`) {
		t.Fatalf("exe or state folder is not quoted: %s", line)
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
	for i := 0; i < n; i++ {
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
	if !contains(got, "/usr/bin") || !contains(got, "/bin") {
		t.Fatalf("trimmed PATH lost cron's defaults: %v", got)
	}
	// The kept directories are the leading ones, in their order.
	own := func(ds []string) (out []string) {
		for _, d := range ds {
			if !contains(cronPath, d) {
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
	// A short PATH is not touched.
	if short := "/a/bin:/usr/bin:/bin"; !strings.Contains(Line(exe, home, short), "PATH='"+short+"'") {
		t.Fatal("short PATH was changed")
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
	// An entry from before the state folder was named runs the default
	// folder, which may not be this one.
	old := "*/15 * * * * '/opt/ai-usage/bin/ai-usage' collect --quiet >/dev/null 2>&1 # ai-usage\n"
	if got, _ := cron(withTab(old)).Lookup(ctx, exe, home); got != Other {
		t.Fatalf("entry without a state folder: Lookup = %v", got)
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
		errors.New(`crontab: exec: "crontab": executable file not found in $PATH`),
		silent,
		context.DeadlineExceeded,
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
		"crontab: no crontab for alice":                          true, // macOS, Vixie
		"no crontab for alice":                                   true, // cronie
		"crontab: can't open 'alice': No such file or directory": true, // BusyBox
		"crontab: Operation not permitted":                       false,
		"crontab: can't open 'alice': Permission denied":         false,
		"crontab: signal: killed":                                false,
		"crontab: /var/spool/cron: No such file or directory":    false,
		"crontab: exec: \"crontab\": executable file not found":  false,
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

// fakeTasks answers schtasks. A nil query means the task does not exist.
type fakeTasks struct {
	query *string
	calls [][]string
	// task is the definition the last /Create read from its /XML file.
	task []byte
}

func (f *fakeTasks) run(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name != "schtasks" {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	switch args[0] {
	case "/Query":
		if f.query == nil {
			return nil, errors.New("schtasks: ERROR: The system cannot find the file specified.")
		}
		return []byte(*f.query), nil
	case "/Create":
		b, err := os.ReadFile(args[len(args)-1])
		f.task = b
		return nil, err
	case "/Delete":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected schtasks %v", args)
}

// taskXML reads a task definition as schtasks does: UTF-16LE with a BOM.
func taskXML(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE || len(b)%2 != 0 {
		t.Fatalf("task definition is not UTF-16LE with a BOM: % x", b[:min(len(b), 8)])
	}
	return decodeText(b)
}

func TestWindows(t *testing.T) {
	ctx := context.Background()
	winExe := `C:\Users\A B\AppData\Local\Programs\ai-usage\ai-usage.exe`
	winHome := `C:\Users\A B\AppData\Local\ai-usage`

	f := &fakeTasks{}
	s := Scheduler{GOOS: "windows", Run: f.run}
	if err := s.Install(ctx, winExe, winHome, "ignored"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("Install ran %q", f.calls)
	}
	file := f.calls[0][len(f.calls[0])-1]
	if want := []string{"schtasks", "/Create", "/F", "/TN", "ai-usage", "/XML", file}; !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("Install ran %q, want %q", f.calls[0], want)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("task definition %s was left behind", file)
	}
	task := taskXML(t, f.task)
	var def struct {
		XMLName  xml.Name `xml:"Task"`
		Triggers struct {
			Time struct {
				Start    string `xml:"StartBoundary"`
				Interval string `xml:"Repetition>Interval"`
				Duration string `xml:"Repetition>Duration"`
			} `xml:"TimeTrigger"`
		} `xml:"Triggers"`
		Settings struct {
			Multiple        string `xml:"MultipleInstancesPolicy"`
			NoBattery       string `xml:"DisallowStartIfOnBatteries"`
			StopOnBattery   string `xml:"StopIfGoingOnBatteries"`
			StartWhenMissed string `xml:"StartWhenAvailable"`
			TimeLimit       string `xml:"ExecutionTimeLimit"`
			Enabled         string `xml:"Enabled"`
			OnlyIfNetworkUp string `xml:"RunOnlyIfNetworkAvailable"`
		} `xml:"Settings"`
		Exec struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Actions>Exec"`
	}
	// encoding/xml reads UTF-8; the declaration names the file's UTF-16.
	body := strings.Replace(task, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(body), &def); err != nil {
		t.Fatalf("task definition: %v\n%s", err, task)
	}
	st := def.Settings
	if st.NoBattery != "false" || st.StopOnBattery != "false" || st.StartWhenMissed != "true" ||
		st.Multiple != "IgnoreNew" || st.TimeLimit != "PT10M" || st.Enabled != "true" || st.OnlyIfNetworkUp != "false" {
		t.Fatalf("task settings = %+v", st)
	}
	if tr := def.Triggers.Time; tr.Interval != "PT15M" || tr.Duration != "" || tr.Start == "" {
		t.Fatalf("task trigger = %+v", tr)
	}
	if def.Exec.Command != winExe || def.Exec.Arguments != `collect --quiet --home "`+winHome+`"` {
		t.Fatalf("task action = %+v", def.Exec)
	}

	// The task runs this binary and folder when its token matches. schtasks
	// prints the path in the console code page (0x81 is ü in CP850), which
	// must not matter.
	umlautExe := "C:\\Users\\J\u00fcrgen\\ai-usage.exe"
	umlautHome := "C:\\Users\\J\u00fcrgen\\ai-usage-state"
	cp850 := strings.ReplaceAll(TaskXML(umlautExe, umlautHome), "\u00fc", "\x81")
	registered := TaskXML(winExe, winHome)
	disabled := strings.Replace(registered, "<Enabled>true</Enabled>\n    <RunOnlyIfIdle>", "<Enabled>false</Enabled>\n    <RunOnlyIfIdle>", 1)
	utf16 := string(utf16LE(registered))
	for _, tc := range []struct {
		name, query, exe, home string
		want                   State
	}{
		{"this binary", registered, winExe, winHome, Active},
		{"other case", registered, strings.ToLower(winExe), strings.ToUpper(winHome), Active},
		{"UTF-16 output", utf16, winExe, winHome, Active},
		{"code page output", cp850, umlautExe, umlautHome, Active},
		{"other binary", registered, `C:\other\ai-usage.exe`, winHome, Other},
		{"other state folder", registered, winExe, `D:\ai-usage`, Other},
		{"made by schtasks /SC", "<Task><Actions><Exec><Command>" + winExe + "</Command></Exec></Actions></Task>", winExe, winHome, Other},
		{"disabled", disabled, winExe, winHome, Disabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.query
			f := &fakeTasks{query: &q}
			got, err := Scheduler{GOOS: "windows", Run: f.run}.Lookup(ctx, tc.exe, tc.home)
			if err != nil || got != tc.want {
				t.Fatalf("Lookup = %v, %v; want %v", got, err, tc.want)
			}
			if want := []string{"schtasks", "/Query", "/TN", "ai-usage", "/XML"}; !reflect.DeepEqual(f.calls[0], want) {
				t.Fatalf("Lookup ran %q, want %q", f.calls[0], want)
			}
		})
	}
	if disabled == registered {
		t.Fatal("test did not disable the task")
	}

	f = &fakeTasks{query: &registered}
	s.Run = f.run
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || !reflect.DeepEqual(f.calls[1], []string{"schtasks", "/Delete", "/F", "/TN", "ai-usage"}) {
		t.Fatalf("Remove ran %q", f.calls)
	}

	// Removing a task that does not exist does not try to delete it.
	f = &fakeTasks{}
	s.Run = f.run
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Lookup(ctx, winExe, winHome); got != Absent || err != nil {
		t.Fatalf("Lookup with no task = %v, %v", got, err)
	}
	for _, c := range f.calls {
		if c[1] == "/Delete" {
			t.Fatalf("Remove deleted an absent task: %q", f.calls)
		}
	}
}

func TestWinQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\a b\state`: `"C:\a b\state"`,
		`C:\`:          `"C:\\"`,
		`\\srv\x\\`:    `"\\srv\x\\\\"`,
	} {
		if got := winQuote(in); got != want {
			t.Errorf("winQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AIU_SCHEDULE_HELPER", "echo")
	out, err := execRunner(ctx, os.Args[0], helperArgs, []byte("MAILTO=x\n"))
	if err != nil || string(out) != "MAILTO=x\n" {
		t.Fatalf("echo = %q, %v", out, err)
	}

	t.Setenv("AIU_SCHEDULE_HELPER", "no-crontab")
	_, err = execRunner(ctx, os.Args[0], helperArgs, nil)
	if err == nil || !strings.HasSuffix(err.Error(), ": crontab: no crontab for tester") {
		t.Fatalf("stderr is not the error: %v", err)
	}
	if !noCrontab(err.Error()) {
		t.Fatal("no crontab is not recognized through the runner")
	}
}

var helperArgs = []string{"-test.run=^TestHelperProcess$"}

// silentExit is a command that fails with no output at all, as a killed or
// timed-out crontab does.
func silentExit(t *testing.T) error {
	t.Setenv("AIU_SCHEDULE_HELPER", "silent")
	out, err := execRunner(context.Background(), os.Args[0], helperArgs, nil)
	if err == nil || len(out) != 0 {
		t.Fatalf("silent helper = %q, %v", out, err)
	}
	return err
}

// TestHelperProcess stands in for crontab when the test binary runs itself.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("AIU_SCHEDULE_HELPER") {
	case "":
		return
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "no-crontab":
		fmt.Fprint(os.Stderr, "crontab: no crontab for tester\nsecond line\n")
		os.Exit(1)
	case "silent":
		os.Exit(1)
	}
	os.Exit(2)
}

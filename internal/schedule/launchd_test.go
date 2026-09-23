package schedule

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeLaunchd answers launchctl for user 501 and keeps a crontab for the
// line an older version wrote.
type fakeLaunchd struct {
	noSession bool
	loaded    bool
	// lingering is how many checks still find the agent loaded after a
	// bootout, as while launchd stops a run.
	lingering int
	// disabled is what print-disabled shows for the agent; empty means it is
	// not listed.
	disabled string
	cron     fakeCron
	calls    []string
	// booted is the plist the last bootstrap loaded.
	booted string
}

func (f *fakeLaunchd) run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	if name == "crontab" {
		return f.cron.run(ctx, name, args, stdin)
	}
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "launchctl" || len(args) == 0 {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	service := "gui/501/" + AgentLabel
	switch {
	case reflect.DeepEqual(args, []string{"print", "gui/501"}):
		if f.noSession {
			return nil, errors.New("launchctl: Could not find domain for port")
		}
		return []byte("gui/501 = {\n}\n"), nil
	case reflect.DeepEqual(args, []string{"print", service}):
		if !f.loaded && f.lingering > 0 {
			f.lingering--
			return []byte(service + " = {\n}\n"), nil
		}
		if !f.loaded {
			return nil, errors.New(`launchctl: Could not find service "` + AgentLabel + `" in domain for user gui: 501`)
		}
		return []byte(service + " = {\n}\n"), nil
	case reflect.DeepEqual(args, []string{"print-disabled", "gui/501"}):
		out := "\tdisabled services = {\n\t\t\"com.example.other\" => disabled\n"
		if f.disabled != "" {
			out += "\t\t\"" + AgentLabel + "\" => " + f.disabled + "\n"
		}
		return []byte(out + "\t}\n"), nil
	case reflect.DeepEqual(args, []string{"bootout", service}):
		if !f.loaded {
			return nil, errors.New("launchctl: Boot-out failed: 3: No such process")
		}
		f.loaded = false
		return nil, nil
	case reflect.DeepEqual(args, []string{"enable", service}):
		f.disabled = "enabled"
		return nil, nil
	case len(args) == 3 && args[0] == "bootstrap" && args[1] == "gui/501":
		if f.loaded || f.lingering > 0 {
			return nil, errors.New("launchctl: Bootstrap failed: 5: Input/output error")
		}
		if f.disabled == "disabled" {
			return nil, errors.New("launchctl: Bootstrap failed: 119: Service is disabled")
		}
		b, err := os.ReadFile(args[2])
		if err != nil {
			return nil, err
		}
		f.booted, f.loaded = string(b), true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected launchctl %v", args)
}

func (f *fakeLaunchd) ran(call string) bool { return slices.Contains(f.calls, call) }

func agent(t *testing.T, f *fakeLaunchd) (Scheduler, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Library", "LaunchAgents")
	return Scheduler{GOOS: "darwin", Run: f.run, AgentDir: dir, UID: 501}, filepath.Join(dir, AgentLabel+".plist")
}

// plistValues reads the launch agent's keys the way launchd does, with
// plutil on macOS, and with a small plist reader elsewhere.
func plistValues(t *testing.T, file string) map[string]any {
	t.Helper()
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint: %v %s", err, out)
		}
		out, err := exec.Command("plutil", "-convert", "json", "-o", "-", file).Output()
		if err != nil {
			t.Fatalf("plutil -convert: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return readPlist(t, b)
}

// readPlist decodes the dict, string, integer, and array values the launch
// agent uses.
func readPlist(t *testing.T, b []byte) map[string]any {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = true
	var value func(start xml.StartElement) any
	value = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "string", "integer", "key":
			var s string
			if err := d.DecodeElement(&s, &start); err != nil {
				t.Fatal(err)
			}
			if start.Name.Local == "integer" {
				var n float64
				if _, err := fmt.Sscan(s, &n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			return s
		case "array", "dict":
			var list []any
			m := map[string]any{}
			key := ""
			for {
				tok, err := d.Token()
				if err != nil {
					t.Fatal(err)
				}
				switch tok := tok.(type) {
				case xml.StartElement:
					v := value(tok)
					switch {
					case start.Name.Local == "array":
						list = append(list, v)
					case tok.Name.Local == "key":
						key = v.(string)
					default:
						m[key] = v
					}
				case xml.EndElement:
					if start.Name.Local == "array" {
						return list
					}
					return m
				}
			}
		}
		t.Fatalf("unexpected plist element %s", start.Name.Local)
		return nil
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			t.Fatal("plist has no dict")
		}
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			return value(se).(map[string]any)
		}
	}
}

func TestAgentPlist(t *testing.T) {
	odd := "/Users/o'brien/AI & <Tools> 100%/ai-usage"
	state := "/Users/o'brien/state \"x\""
	file := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(file, []byte(AgentPlist(odd, state, "/opt/a&b/bin:/usr/bin:rel::/opt/a&b/bin")), 0o644); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Label":                AgentLabel,
		"ProgramArguments":     []any{odd, "collect", "--quiet", "--home", state},
		"EnvironmentVariables": map[string]any{"PATH": "/opt/a&b/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		"StartCalendarInterval": []any{
			map[string]any{"Minute": float64(0)}, map[string]any{"Minute": float64(15)},
			map[string]any{"Minute": float64(30)}, map[string]any{"Minute": float64(45)},
		},
		"ProcessType":       "Background",
		"StandardOutPath":   "/dev/null",
		"StandardErrorPath": "/dev/null",
	}
	if got := plistValues(t, file); !reflect.DeepEqual(got, want) {
		t.Fatalf("plist:\n got %v\nwant %v", got, want)
	}
}

func TestAgentInstall(t *testing.T) {
	ctx := context.Background()
	f := &fakeLaunchd{}
	s, file := agent(t, f)
	if got, err := s.Lookup(ctx, exe, home); err != nil || got != Absent {
		t.Fatalf("Lookup before Install = %v, %v", got, err)
	}
	if err := s.Install(ctx, exe, home, "/usr/local/bin:/usr/bin"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != AgentPlist(exe, home, "/usr/local/bin:/usr/bin") || f.booted != string(b) {
		t.Fatalf("plist:\n%s\nbooted:\n%s", b, f.booted)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("plist mode = %v, %v", fi.Mode(), err)
	}
	if f.ran("launchctl bootout gui/501/" + AgentLabel) {
		t.Fatal("an agent that was not loaded was booted out")
	}
	if got, err := s.Lookup(ctx, exe, home); err != nil || got != Active {
		t.Fatalf("Lookup after Install = %v, %v", got, err)
	}
	if got, _ := s.Lookup(ctx, exe, "/other/state"); got != Other {
		t.Fatalf("Lookup for another state folder = %v", got)
	}
	// A crontab with no line of ours is never written, because writing it
	// brings up the macOS prompt.
	if f.cron.writes() != 0 {
		t.Fatal("crontab was written")
	}

	// Installing again reloads the new definition in place of the old one.
	f.calls = nil
	if err := s.Install(ctx, exe, "/new/state", "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	out := slices.Index(f.calls, "launchctl bootout gui/501/"+AgentLabel)
	boot := slices.Index(f.calls, "launchctl bootstrap gui/501 "+file)
	if out < 0 || boot < out {
		t.Fatalf("reinstall calls: %q", f.calls)
	}
	if !strings.Contains(f.booted, "<string>/new/state</string>") {
		t.Fatalf("the old definition stayed loaded:\n%s", f.booted)
	}
	if got, _ := s.Lookup(ctx, exe, "/new/state"); got != Active {
		t.Fatalf("Lookup after reinstall = %v", got)
	}
	// No temporary plist is left beside the agent.
	entries, _ := os.ReadDir(s.AgentDir)
	if len(entries) != 1 {
		t.Fatalf("LaunchAgents holds %d files", len(entries))
	}
}

func TestAgentLookup(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		plist    string
		loaded   bool
		disabled string
		want     State
	}{
		{"none", "", false, "", Absent},
		{"loaded", AgentPlist(exe, home, ""), true, "", Active},
		{"enabled by hand", AgentPlist(exe, home, ""), true, "enabled", Active},
		{"not loaded", AgentPlist(exe, home, ""), false, "", Absent},
		{"disabled", AgentPlist(exe, home, ""), false, "disabled", Disabled},
		{"disabled on older macOS", AgentPlist(exe, home, ""), true, "true", Disabled},
		{"disabled and another binary", AgentPlist("/old/ai-usage", home, ""), false, "disabled", Disabled},
		{"another binary", AgentPlist("/old/ai-usage", home, ""), true, "", Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLaunchd{loaded: tc.loaded, disabled: tc.disabled}
			s, file := agent(t, f)
			if tc.plist != "" {
				if err := os.MkdirAll(s.AgentDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(tc.plist), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.Lookup(ctx, exe, home); err != nil || got != tc.want {
				t.Fatalf("Lookup = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestAgentInstallTurnsDisabledBackOn(t *testing.T) {
	ctx := context.Background()
	f := &fakeLaunchd{disabled: "disabled"}
	s, _ := agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Active {
		t.Fatalf("Lookup = %v, calls %q", got, f.calls)
	}
}

func TestAgentWithoutLoginSession(t *testing.T) {
	f := &fakeLaunchd{noSession: true}
	s, file := agent(t, f)
	err := s.Install(context.Background(), exe, home, "/usr/bin")
	if err == nil || !strings.Contains(err.Error(), "log in at the screen") {
		t.Fatalf("Install = %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist written with no session: %v", err)
	}
}

func TestAgentWithoutHomeFolder(t *testing.T) {
	f := &fakeLaunchd{}
	s := Scheduler{GOOS: "darwin", Run: f.run, UID: 501}
	ctx := context.Background()
	if err := s.Install(ctx, exe, home, "/usr/bin"); err == nil {
		t.Fatal("Install with no LaunchAgents folder succeeded")
	}
	if _, err := s.Lookup(ctx, exe, home); err == nil {
		t.Fatal("Lookup with no LaunchAgents folder succeeded")
	}
	if len(f.calls) != 0 {
		t.Fatalf("launchctl ran: %q", f.calls)
	}
}

func TestAgentRemove(t *testing.T) {
	ctx := context.Background()
	f := &fakeLaunchd{}
	s, file := agent(t, f)
	// Removing an agent that was never installed is not an error.
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if f.loaded {
		t.Fatal("agent is still loaded")
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist stays: %v", err)
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Absent {
		t.Fatalf("Lookup after Remove = %v", got)
	}
}

// TestAgentReplacesCronLine moves a Mac off the crontab line an older version
// wrote, keeping the person's own lines.
func TestAgentReplacesCronLine(t *testing.T) {
	ctx := context.Background()
	mine := "0 3 * * * /usr/local/bin/backup\n"
	old := mine + Line(exe, home, "/usr/bin:/bin") + "\n"

	f := &fakeLaunchd{cron: *withTab(old)}
	s, _ := agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if *f.cron.tab != mine {
		t.Fatalf("crontab after Install:\n%s", *f.cron.tab)
	}

	f = &fakeLaunchd{cron: *withTab(old)}
	s, _ = agent(t, f)
	if err := s.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.cron.tab != mine {
		t.Fatalf("crontab after Remove:\n%s", *f.cron.tab)
	}

	// A crontab that cannot be read is left alone.
	f = &fakeLaunchd{cron: fakeCron{readErr: errors.New("crontab: signal: killed")}}
	s, _ = agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if f.cron.writes() != 0 {
		t.Fatal("an unread crontab was written")
	}
}

// TestAgentRunDoesNotBootItselfOut: a run launchd started that finds the
// plist gone writes it back without booting the agent out, which would stop
// the run before it loaded the agent again.
func TestAgentRunDoesNotBootItselfOut(t *testing.T) {
	ctx := context.Background()
	f := &fakeLaunchd{}
	s, file := agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	s.InAgent = true
	if got, _ := s.Lookup(ctx, exe, home); got != Absent {
		t.Fatalf("Lookup with the plist gone = %v", got)
	}
	f.calls = nil
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "launchctl print") {
			t.Fatalf("a run of the agent ran %q", c)
		}
	}
	if !f.loaded {
		t.Fatal("agent unloaded")
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Active {
		t.Fatalf("Lookup after the plist was written back = %v", got)
	}
}

// TestAgentWaitsForBootout: bootstrap runs only once launchd has stopped the
// old agent.
func TestAgentWaitsForBootout(t *testing.T) {
	defer func(d time.Duration) { settle = d }(settle)
	settle = time.Millisecond
	ctx := context.Background()
	f := &fakeLaunchd{}
	s, _ := agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	f.lingering = 3
	if err := s.Install(ctx, exe, "/new/state", "/usr/bin"); err != nil {
		t.Fatalf("Install while the old agent stops: %v", err)
	}
	if got, _ := s.Lookup(ctx, exe, "/new/state"); got != Active {
		t.Fatalf("Lookup = %v", got)
	}
	f.lingering = 1000
	if err := s.Remove(ctx); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("Remove of an agent that never stops = %v", err)
	}
}

// TestAgentLeftoverCronLine: a crontab line an older version wrote keeps the
// schedule from reading as healthy until a run the person started removes
// it, and a line they commented out keeps the collector paused.
func TestAgentLeftoverCronLine(t *testing.T) {
	ctx := context.Background()
	mine := "0 3 * * * /usr/local/bin/backup\n"
	live := mine + Line(exe, home, "/usr/bin:/bin") + "\n"

	// Removing the line failed once, as when the person declined the prompt.
	f := &fakeLaunchd{cron: fakeCron{tab: &live, writeErr: errors.New("crontab: signal: killed")}}
	s, file := agent(t, f)
	if err := s.Install(ctx, exe, home, "/usr/bin"); err == nil || !strings.Contains(err.Error(), "crontab line") {
		t.Fatalf("Install = %v", err)
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Duplicate {
		t.Fatalf("Lookup with the line left = %v", got)
	}
	// A run of the agent reports it and does not write the crontab.
	s.InAgent = true
	f.calls, f.cron.calls = nil, nil
	if err := s.Install(ctx, exe, home, "/usr/bin"); err == nil || !strings.Contains(err.Error(), "schedule install") {
		t.Fatalf("Install from the agent = %v", err)
	}
	if f.cron.writes() != 0 || f.ran("launchctl bootout gui/501/"+AgentLabel) {
		t.Fatalf("the agent run wrote the crontab or booted out: %q %q", f.calls, f.cron.calls)
	}
	// A run the person started removes it.
	s.InAgent = false
	f.cron.writeErr = nil
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if *f.cron.tab != mine {
		t.Fatalf("crontab:\n%s", *f.cron.tab)
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Active {
		t.Fatalf("Lookup = %v", got)
	}

	// Remove stops before the agent when the crontab cannot be written.
	f.cron.tab, f.cron.writeErr = &live, errors.New("crontab: signal: killed")
	if err := s.Remove(ctx); err == nil {
		t.Fatal("Remove ignored the crontab failure")
	}
	if _, err := os.Stat(file); err != nil || !f.loaded {
		t.Fatalf("Remove took the agent down anyway: loaded %v, %v", f.loaded, err)
	}

	// A line commented out by hand on an older version stays a pause.
	paused := mine + "# " + Line(exe, home, "/usr/bin:/bin") + "\n"
	f = &fakeLaunchd{cron: fakeCron{tab: &paused}}
	s, _ = agent(t, f)
	if got, _ := s.Lookup(ctx, exe, home); got != Disabled {
		t.Fatalf("Lookup with a commented line = %v", got)
	}
	// Installing turns it back on and drops the old line.
	if err := s.Install(ctx, exe, home, "/usr/bin"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Lookup(ctx, exe, home); got != Active || *f.cron.tab != mine {
		t.Fatalf("Lookup = %v, crontab:\n%s", got, *f.cron.tab)
	}
}

package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeLaunchd answers launchctl for user 501 and fails any other command,
// crontab included.
type fakeLaunchd struct {
	noSession bool
	loaded    bool
	// lingering is how many checks still find the agent loaded after a
	// bootout, as while launchd stops a run.
	lingering int
	// disabled is what print-disabled shows for the agent; empty means it is
	// not listed.
	disabled string
	// pinned says launchd pinned the loaded agent to its binary's code
	// signature, as it does at login on macOS 27, and replaced that an
	// update replaced the binary since: launchd refuses the next start, and
	// repairs the pin at the start after. refused says it refused the last
	// start; stubborn, that it refuses every start of a pinned agent; stuck,
	// that a kickstart starts nothing; running, that the agent runs now.
	pinned, replaced, refused, stubborn, stuck, running bool
	// runs is how many times launchd started the agent or tried to.
	runs int
	// spawning is how many prints after a kickstart show the agent on its
	// way to run, with the last start's exit still, as while launchd spawns
	// it; spawned is what those prints show.
	spawning int
	spawned  struct {
		left    int
		refused bool
	}
	calls []string
	// booted is the plist the last bootstrap loaded.
	booted string
}

// printOf is launchctl print of the agent on macOS 27, as testdata has it,
// with its runs count set to runs and, unless state is empty, its state set
// to state: refused, of an agent launchd loaded at login and then refused to
// start once an update had replaced its binary; refused-first, of one it
// refused at its first start since it loaded it at login; pinned, of one
// launchd loaded at login and started; and unpinned, of one loaded from the
// command line and not started.
func printOf(name string, runs int, state string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join("testdata", "print-"+name+".txt"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "\truns = "):
			lines[i] = "\truns = " + strconv.Itoa(runs)
		case strings.HasPrefix(l, "\tstate = ") && state != "":
			lines[i] = "\tstate = " + state
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func (f *fakeLaunchd) run(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
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
		state, refused := "", f.refused
		switch {
		case f.spawned.left > 0:
			f.spawned.left--
			state, refused = "xpcproxy", f.spawned.refused
		case f.running:
			state = "running"
		}
		switch {
		case refused:
			return printOf("refused", f.runs, state)
		case f.pinned:
			return printOf("pinned", f.runs, state)
		}
		return printOf("unpinned", f.runs, state)
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
		// A job loaded from the command line is pinned to nothing.
		f.booted, f.loaded = string(b), true
		f.pinned, f.refused, f.runs = false, false, 0
		return nil, nil
	case reflect.DeepEqual(args, []string{"kickstart", service}):
		if !f.loaded {
			return nil, errors.New(`launchctl: Could not find service "` + AgentLabel + `" in domain for user gui: 501`)
		}
		if f.stuck || f.running {
			return nil, nil
		}
		f.runs++
		f.spawned.left, f.spawned.refused = f.spawning, f.refused
		f.refused = f.pinned && (f.stubborn || f.replaced && !f.refused)
		if !f.refused {
			f.replaced = false
		}
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

// plistValues reads the launch agent's keys the way launchd does, with plutil.
func plistValues(t *testing.T, file string) map[string]any {
	t.Helper()
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

func TestAgentPlist(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil reads the plist as launchd does; CI runs this on macOS")
	}
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
	// The crontab is never touched, because writing it brings up the macOS
	// prompt.
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "crontab ") }) {
		t.Fatalf("crontab ran: %q", f.calls)
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

func TestReadPrint(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		drop        []string
		want        agentPrint
	}{
		{"refused", "", nil, agentPrint{runs: 3, idle: true, ended: true, pinned: true, refused: true}},
		// Refused at its first start since launchd loaded it at login, the
		// agent prints the refusal as its last exit's reason, and a moment
		// later as its job state too.
		{"refused-first", "", nil, agentPrint{runs: 3, idle: true, ended: true, pinned: true, refused: true}},
		{"refused-first", "", []string{"\tjob state = "}, agentPrint{runs: 3, idle: true, ended: true, pinned: true, refused: true}},
		{"refused-first", "", []string{"\tjob state = ", "\tlast exit reason = "}, agentPrint{runs: 3, idle: true, pinned: true}},
		{"pinned", "", nil, agentPrint{runs: 3, idle: true, ended: true, pinned: true}},
		{"unpinned", "", nil, agentPrint{runs: 3, idle: true}},
		// While launchd spawns the agent again, and once it runs, the
		// refusal before is still the last exit.
		{"refused", "xpcproxy", nil, agentPrint{runs: 3, ended: true, pinned: true}},
		{"refused", "running", nil, agentPrint{runs: 3, running: true, ended: true, pinned: true}},
	} {
		b, err := printOf(tc.name, 3, tc.state)
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for l := range strings.SplitSeq(string(b), "\n") {
			if !slices.ContainsFunc(tc.drop, func(d string) bool { return strings.HasPrefix(l, d) }) {
				kept = append(kept, l)
			}
		}
		b = []byte(strings.Join(kept, "\n"))
		if got := readPrint(b); got != tc.want {
			t.Errorf("%s (state %q, without %q) = %+v, want %+v", tc.name, tc.state, tc.drop, got, tc.want)
		}
	}
	if got := readPrint([]byte("gui/501/" + AgentLabel + " = {\n}\n")); got != (agentPrint{}) {
		t.Errorf("an empty print = %+v", got)
	}
}

func TestAgentRefused(t *testing.T) {
	ctx := context.Background()
	f := &fakeLaunchd{}
	s, _ := agent(t, f)
	if err := s.Install(ctx, exe, home, "/opt/homebrew/bin:/usr/bin"); err != nil {
		t.Fatal(err)
	}
	f.pinned, f.refused, f.runs = true, true, 1
	if got, err := s.Lookup(ctx, exe, home); err != nil || got != Refused {
		t.Fatalf("Lookup = %v, %v", got, err)
	}
	// A run the agent started was not refused.
	in := s
	in.InAgent = true
	if got, _ := in.Lookup(ctx, exe, home); got != Active {
		t.Fatalf("Lookup in a run of the agent = %v", got)
	}
	// Another binary's agent stays another's, refused or not.
	if got, _ := s.Lookup(ctx, "/old/ai-usage", home); got != Other {
		t.Fatalf("Lookup for another binary = %v", got)
	}
}

// Repair has launchd start a pinned agent whose binary was just replaced,
// which it refuses at once and repairs at the start after, and a refused
// agent once. It leaves alone an agent pinned to nothing, one that runs, one
// launchd does not start, and every agent from a run the agent started.
func TestAgentRepair(t *testing.T) {
	defer func(d time.Duration) { settle = d }(settle)
	settle = time.Millisecond
	svc := "gui/501/" + AgentLabel
	print, kick := "launchctl print "+svc, "launchctl kickstart "+svc
	for _, tc := range []struct {
		name     string
		set      func(*fakeLaunchd)
		replaced bool
		inAgent  bool
		calls    []string
		err      error
		runs     int
		after    State
	}{
		{"replaced and pinned", func(f *fakeLaunchd) { f.pinned, f.replaced = true, true }, true, false,
			[]string{print, kick, print, kick, print}, nil, 2, Active},
		{"replaced and pinned to nothing", func(f *fakeLaunchd) {}, true, false,
			[]string{print}, nil, 0, Active},
		{"not replaced", func(f *fakeLaunchd) { f.pinned = true }, false, false,
			[]string{print}, nil, 0, Active},
		{"refused", func(f *fakeLaunchd) { f.pinned, f.refused, f.runs = true, true, 1 }, false, false,
			[]string{print, kick, print}, nil, 2, Active},
		{"replaced and refused", func(f *fakeLaunchd) { f.pinned, f.refused, f.runs = true, true, 1 }, true, false,
			[]string{print, kick, print}, nil, 2, Active},
		{"still refused", func(f *fakeLaunchd) { f.pinned, f.refused, f.stubborn, f.runs = true, true, true, 1 }, false, false,
			[]string{print, kick, print}, ErrRefused, 2, Refused},
		{"refused, and spawned slowly", func(f *fakeLaunchd) { f.pinned, f.refused, f.runs, f.spawning = true, true, 1, 3 }, false, false,
			[]string{print, kick, print, print, print, print}, nil, 2, Active},
		{"replaced and pinned, and spawned slowly", func(f *fakeLaunchd) { f.pinned, f.replaced, f.spawning = true, true, 2 }, true, false,
			[]string{print, kick, print, print, print, kick, print, print, print}, nil, 2, Active},
		{"replaced and launchd starts nothing", func(f *fakeLaunchd) { f.pinned, f.replaced, f.stuck = true, true, true }, true, false,
			append([]string{print, kick}, slices.Repeat([]string{print}, 50)...), nil, 0, Active},
		{"replaced while it runs", func(f *fakeLaunchd) { f.pinned, f.replaced, f.running = true, true, true }, true, false,
			[]string{print}, nil, 0, Active},
		{"not loaded", func(f *fakeLaunchd) { f.loaded = false }, true, false,
			[]string{print}, nil, 0, Absent},
		{"in a run of the agent", func(f *fakeLaunchd) { f.pinned, f.refused, f.runs = true, true, 1 }, false, true,
			nil, nil, 1, Refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := &fakeLaunchd{}
			s, file := agent(t, f)
			if err := s.Install(ctx, exe, home, "/opt/homebrew/bin:/usr/bin"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			tc.set(f)
			pinned := f.pinned
			s.InAgent = tc.inAgent
			f.calls = nil
			if err := s.Repair(ctx, tc.replaced); !errors.Is(err, tc.err) {
				t.Fatalf("Repair = %v, want %v", err, tc.err)
			}
			if !reflect.DeepEqual(f.calls, tc.calls) {
				t.Fatalf("calls:\n got %q\nwant %q", f.calls, tc.calls)
			}
			// The agent keeps its pin and its plist.
			after, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(after, before) || f.runs != tc.runs || f.pinned != pinned {
				t.Fatalf("after Repair: runs %d, want %d; pinned %v, was %v; plist changed %v", f.runs, tc.runs, f.pinned, pinned, !bytes.Equal(after, before))
			}
			s.InAgent = false
			if got, _ := s.Lookup(ctx, exe, home); got != tc.after {
				t.Fatalf("Lookup after Repair = %v, want %v", got, tc.after)
			}
		})
	}
}

// TestRepairElsewhere: cron and Task Scheduler have nothing to repair.
func TestRepairElsewhere(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		s := Scheduler{GOOS: goos, Run: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			t.Errorf("%s ran %s %v", goos, name, args)
			return nil, errors.New("no scheduler")
		}}
		if err := s.Repair(context.Background(), true); err != nil {
			t.Fatalf("%s: Repair = %v", goos, err)
		}
	}
}

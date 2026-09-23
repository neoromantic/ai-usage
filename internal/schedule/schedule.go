// Package schedule registers the collector with the system scheduler: the
// user's crontab on Linux, a launch agent on macOS, Task Scheduler on Windows.
package schedule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// Marker ends the crontab line and names the Windows task.
const Marker = "ai-usage"

// Interval is how often the scheduler runs the collector.
const Interval = 15 * time.Minute

// maxLine keeps the crontab entry inside the 1000-byte command buffer of
// Vixie-derived crons (macOS, Debian), which cut a longer command silently.
const maxLine = 990

// cronPath is cron's own PATH. It is kept when a long PATH is shortened, so
// the collector can still find crontab and the tools harness scripts call.
var cronPath = []string{"/usr/bin", "/bin"}

// State is what the scheduler holds for the collector.
type State int

const (
	// Absent means there is no entry.
	Absent State = iota
	// Other means an entry runs another binary or another state folder.
	Other
	// Duplicate means the launch agent runs the collector, and a crontab
	// line an older version wrote on macOS runs it too.
	Duplicate
	// Disabled means the person commented the entry out or disabled the
	// task or the launch agent. Runs leave it alone; Install turns it back
	// on.
	Disabled
	// Active means the entry runs this binary with this state folder.
	Active
)

// Runner runs a scheduler command. Tests replace it.
type Runner func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)

// Scheduler talks to the OS scheduler.
type Scheduler struct {
	GOOS string
	Run  Runner
	// AgentDir holds the macOS launch agent, and UID names the login
	// session launchd runs it in. InAgent says this process is a run the
	// launch agent started.
	AgentDir string
	UID      int
	InAgent  bool
}

// Default uses the real OS and commands.
func Default() Scheduler {
	s := Scheduler{GOOS: runtime.GOOS, Run: execRunner, UID: os.Getuid(), InAgent: os.Getenv("XPC_SERVICE_NAME") == AgentLabel}
	if h, err := os.UserHomeDir(); err == nil {
		s.AgentDir = filepath.Join(h, "Library", "LaunchAgents")
	}
	return s
}

func execRunner(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%s: %s", name, firstLine(msg))
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

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
			if !contains(out, d) {
				out = append(out, d)
			}
		}
		return out
	}
	var kept []string
	full := false
	for _, d := range dirs {
		switch {
		case contains(cronPath, d):
			kept = append(kept, d)
		case full:
		case len(cronLine(exe, home, withRequired(append(kept[:len(kept):len(kept)], d)))) > maxLine:
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
	a := "collect --quiet"
	if home != "" {
		a += " --home " + quote(home)
	}
	return a
}

// pathDirs drops empty and relative PATH entries, which would resolve against
// cron's working directory, and repeats, which never win a lookup.
func pathDirs(path string) []string {
	var out []string
	for _, d := range strings.Split(path, ":") {
		if !strings.HasPrefix(d, "/") || contains(out, d) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Install registers exe to collect into the state folder home. It replaces
// an earlier registration, including one the person disabled.
func (s Scheduler) Install(ctx context.Context, exe, home, path string) error {
	if strings.ContainsAny(exe+home+path, "\r\n") {
		// A line break would end the entry and start another one.
		return errors.New("cannot schedule a binary path, state folder, or PATH that contains a line break")
	}
	switch s.GOOS {
	case "windows":
		return s.installTask(ctx, exe, home)
	case "darwin":
		return s.installAgent(ctx, exe, home, path)
	}
	lines, err := s.cronLines(ctx)
	if err != nil {
		return err
	}
	kept := withoutMarker(lines)
	kept = append(kept, Line(exe, home, path))
	return s.writeCron(ctx, kept)
}

// installTask registers the Windows task from a task definition, because
// schtasks /SC keeps Task Scheduler's defaults: start only on AC power, stop
// on battery, and skip a run missed while the machine slept.
func (s Scheduler) installTask(ctx context.Context, exe, home string) error {
	f, err := os.CreateTemp("", "ai-usage-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(utf16LE(TaskXML(exe, home)))
	if cerr := f.Close(); werr != nil || cerr != nil {
		return errors.Join(werr, cerr)
	}
	_, err = s.Run(ctx, "schtasks", []string{"/Create", "/F", "/TN", Marker, "/XML", f.Name()}, nil)
	return err
}

// TaskXML is the Windows task: every 15 minutes from a fixed quarter hour,
// on battery too, catching up after sleep, never two at once, and ended
// after 10 minutes. The description carries the job token Lookup matches.
func TaskXML(exe, home string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Collects AI harness usage every 15 minutes. ` + jobToken("windows", exe, home) + `</Description>
  </RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <StartBoundary>2020-01-01T00:00:00</StartBoundary>
      <Repetition>
        <Interval>PT15M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlText(exe) + `</Command>
      <Arguments>` + xmlText(args(home, winQuote)) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// jobToken names one binary and state folder in ASCII. schtasks prints
// paths in the console code page, so a path with a letter like ü or я never
// matches the UTF-8 one; the token matches in any code page.
func jobToken(goos, exe, home string) string {
	if goos == "windows" {
		// Windows paths are not case-sensitive.
		exe, home = strings.ToLower(exe), strings.ToLower(home)
	}
	sum := sha256.Sum256([]byte(exe + "\x00" + home))
	return "[job " + hex.EncodeToString(sum[:8]) + "]"
}

// winQuote quotes one argument for a Windows command line. Backslashes
// before the closing quote are doubled so they do not escape it.
func winQuote(s string) string {
	t := strings.TrimRight(s, `\`)
	return `"` + s + s[len(t):] + `"`
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// utf16LE encodes a task definition the way schtasks /XML reads it.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 2, 2+2*len(units))
	b[0], b[1] = 0xFF, 0xFE
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// Remove deletes the registration. Removing an absent one is not an error.
func (s Scheduler) Remove(ctx context.Context) error {
	if s.GOOS == "darwin" {
		return s.removeAgent(ctx)
	}
	if s.GOOS == "windows" {
		if got, _ := s.Lookup(ctx, "", ""); got == Absent {
			return nil
		}
		_, err := s.Run(ctx, "schtasks", []string{"/Delete", "/F", "/TN", Marker}, nil)
		return err
	}
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

// Lookup reports whether the scheduler runs exe with the state folder home.
func (s Scheduler) Lookup(ctx context.Context, exe, home string) (State, error) {
	if s.GOOS == "darwin" {
		return s.lookupAgent(ctx, exe, home)
	}
	if s.GOOS == "windows" {
		out, err := s.Run(ctx, "schtasks", []string{"/Query", "/TN", Marker, "/XML"}, nil)
		if err != nil {
			return Absent, nil
		}
		task := decodeText(out)
		switch {
		case strings.Contains(task, "<Enabled>false</Enabled>"):
			return Disabled, nil
		case strings.Contains(task, jobToken(s.GOOS, exe, home)):
			return Active, nil
		}
		return Other, nil
	}
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

// decodeText reads schtasks output, which is in the console code page, or
// UTF-16 on some systems.
func decodeText(b []byte) string {
	bom := len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE
	if !bom && (len(b) < 2 || b[1] != 0) {
		return string(b)
	}
	if bom {
		b = b[2:]
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(units))
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

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

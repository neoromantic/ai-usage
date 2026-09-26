package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"unicode/utf16"
)

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

func (s Scheduler) removeTask(ctx context.Context) error {
	if got, _ := s.Lookup(ctx, "", ""); got == Absent {
		return nil
	}
	_, err := s.Run(ctx, "schtasks", []string{"/Delete", "/F", "/TN", Marker}, nil)
	return err
}

func (s Scheduler) lookupTask(ctx context.Context, exe, home string) (State, error) {
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

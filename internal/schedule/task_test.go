package schedule

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

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

const (
	winExe  = `C:\Users\A B\AppData\Local\Programs\ai-usage\ai-usage.exe`
	winHome = `C:\Users\A B\AppData\Local\ai-usage`
)

func TestTaskInstall(t *testing.T) {
	ctx := context.Background()
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
}

func TestTaskLookup(t *testing.T) {
	ctx := context.Background()
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
}

func TestTaskRemove(t *testing.T) {
	ctx := context.Background()
	registered := TaskXML(winExe, winHome)
	f := &fakeTasks{query: &registered}
	s := Scheduler{GOOS: "windows", Run: f.run}
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
		`C:\`:       `"C:\\"`,
		`\\srv\x\\`: `"\\srv\x\\\\"`,
	} {
		if got := winQuote(in); got != want {
			t.Errorf("winQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

package schedule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Odd paths survive the quoting of each scheduler and come back as they
// were written.
var oddJobs = []Entry{
	{"/opt/ai-usage/bin/ai-usage", "/data/ai-usage"},
	{"/Users/ann/Library/My Tools/ai-usage", "/Users/ann/Library/Application Support/ai-usage"},
	{"/home/o'brien/bin/ai-usage", "/home/o'brien/.config/ai 100%/ai-usage"},
	{"/srv/a&b/<ai-usage>", `/srv/back\slash "q"/ai-usage`},
}

func TestEntryReadsTheCrontabLine(t *testing.T) {
	ctx := context.Background()
	if e, err := cron(&fakeCron{}).Entry(ctx); err != nil || e != (Entry{}) {
		t.Fatalf("no crontab: %+v, %v", e, err)
	}
	for _, want := range oddJobs {
		f := withTab("MAILTO=ann@acme.dev\n0 * * * * backup # ai-usage bots\n")
		s := cron(f)
		if err := s.Install(ctx, want.Program, want.Home, "/usr/local/bin:/usr/bin:/bin"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Entry(ctx); err != nil || got != want {
			t.Errorf("Entry = %+v, %v, want %+v", got, err, want)
		}
	}
	// A line the person commented out is read too, and the active one wins.
	f := withTab("#*/15 * * * * '/old/ai-usage' collect --quiet --home '/old/home' >/dev/null 2>&1 # ai-usage\n" +
		Line(exe, home, "") + "\n")
	if got, _ := cron(f).Entry(ctx); got != (Entry{exe, home}) {
		t.Errorf("active after commented: %+v", got)
	}
	f = withTab("#*/15 * * * * '/old/ai-usage' collect --quiet --home '/old/home' >/dev/null 2>&1 # ai-usage\n")
	if got, _ := cron(f).Entry(ctx); got != (Entry{"/old/ai-usage", "/old/home"}) {
		t.Errorf("commented out: %+v", got)
	}
}

func TestEntryReadsThePlist(t *testing.T) {
	ctx := context.Background()
	s, file := agent(t, &fakeLaunchd{})
	if e, err := s.Entry(ctx); err != nil || e != (Entry{}) {
		t.Fatalf("no plist: %+v, %v", e, err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, want := range oddJobs {
		if err := os.WriteFile(file, []byte(AgentPlist(want.Program, want.Home, "/usr/bin:/bin")), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Entry(ctx); err != nil || got != want {
			t.Errorf("Entry = %+v, %v, want %+v", got, err, want)
		}
	}
}

func TestEntryLeavesTheWindowsTask(t *testing.T) {
	s := Scheduler{GOOS: "windows", Run: func(context.Context, string, []string, []byte) ([]byte, error) {
		t.Fatal("ran a command")
		return nil, nil
	}}
	if e, err := s.Entry(context.Background()); err != nil || e != (Entry{}) {
		t.Errorf("Entry = %+v, %v", e, err)
	}
}

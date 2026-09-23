package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Lines are shaped like Grok's logs/unified.jsonl.
func authLine(ts string, pid int, user string) string {
	return fmt.Sprintf(`{"ts":%q,"lvl":"info","pid":%d,"src":"auth","ver":"0.1","msg":"auth init user_info check","ctx":{"key_prefix":"xai-","needs_user_info":false,"rt_prefix":"rt-","user_id":%q}}`, ts, pid, user)
}

func billLine(ts string, pid int, pct float64, config string) string {
	return fmt.Sprintf(`{"ts":%q,"lvl":"info","pid":%d,"src":"billing","ver":"0.1","msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":%g,"historyLen":3,"onDemandCap":null%s},"onDemandEnabled":false,"subscriptionTier":"SuperGrok Plus"}}`, ts, pid, pct, config)
}

const weekly = `,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-16T09:05:45.591751+00:00","end":"2026-09-23T09:05:45.591751+00:00"}`

func grokHome(t *testing.T, files map[string][]string) string {
	t.Helper()
	home := t.TempDir()
	for name, lines := range files {
		writeFile(t, filepath.Join(home, "logs", name), strings.Join(lines, "\n")+"\n")
	}
	return home
}

func TestGrokReading(t *testing.T) {
	home := grokHome(t, map[string][]string{"unified.jsonl": {
		`{"ts":"2026-09-22T11:00:00Z","lvl":"info","pid":1,"msg":"session start","ctx":{}}`,
		authLine("2026-09-22T11:17:50.014Z", 100, "user-a"),
		billLine("2026-09-22T11:18:00Z", 100, 20, weekly),
		authLine("2026-09-22T17:44:54.433Z", 200, "user-a"),
		billLine("2026-09-22T18:13:49.039Z", 200, 35, weekly),
		// Out of order in the file; the timestamp decides.
		billLine("2026-09-22T17:45:30.725Z", 200, 34, weekly),
	}})
	r, err := Grok(home)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 22, 18, 13, 49, 39e6, time.UTC)
	want := Reading{Account: "user-a", Plan: "SuperGrok Plus", Quota: &Quota{At: at, Source: "log", Windows: []snapshot.Window{
		{Name: "7d credits", Percent: 35, Minutes: 10080, ResetsAt: ts("2026-09-23T09:05:45.591751Z")},
	}}}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("got  %+v %v\nwant %+v %v", r, describe(r.Quota), want, describe(want.Quota))
	}
}

func TestGrokAccountAndBilling(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string][]string
		account string
		percent float64 // 0 means no quota
	}{
		{
			name: "latest sign-in by time wins",
			files: map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T12:00:00Z", 2, "user-b"),
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
			}},
			account: "user-b",
		},
		{
			name: "only another user's billing",
			files: map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
				authLine("2026-09-22T11:00:00Z", 2, "user-b"),
				billLine("2026-09-22T12:00:00Z", 1, 90, weekly),
			}},
			account: "user-b",
		},
		{
			// A process still signed in as the previous user refreshes
			// billing later; this user's own older line still counts.
			name: "another user's newer billing does not hide this user's",
			files: map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
				authLine("2026-09-22T11:00:00Z", 2, "user-b"),
				billLine("2026-09-22T11:05:00Z", 2, 15, weekly),
				billLine("2026-09-22T12:00:00Z", 1, 90, weekly),
			}},
			account: "user-b", percent: 15,
		},
		{
			name: "reused pid belongs to the sign-in before the line",
			files: map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T10:00:00Z", 7, "user-a"),
				billLine("2026-09-22T10:05:00Z", 7, 90, weekly),
				authLine("2026-09-22T11:00:00Z", 7, "user-b"),
			}},
			account: "user-b",
		},
		{
			name: "billing from a process with no sign-in left is kept",
			files: map[string][]string{"unified.jsonl": {
				billLine("2026-09-22T09:00:00Z", 5, 42, weekly),
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
			}},
			account: "user-a", percent: 42,
		},
		{
			// The newest billing is in a file read before the one holding
			// its process's sign-in.
			name: "rotated files in any order",
			files: map[string][]string{
				"unified.jsonl": {
					authLine("2026-09-22T11:00:00Z", 30, "user-b"),
					billLine("2026-09-22T11:30:00Z", 30, 15, weekly),
					billLine("2026-09-22T12:00:00Z", 20, 90, weekly),
				},
				"unified.prev.jsonl": {
					authLine("2026-09-22T09:00:00Z", 20, "user-a"),
				},
			},
			account: "user-b", percent: 15,
		},
		{
			name: "broken and partial lines are skipped",
			files: map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
				`{"msg":"auth init user_info check","ctx":{"user_id":`,
				authLine("2026-09-22T11:00:00Z", 2, ""),
				`{"ts":"2026-09-22T11:00:00Z","pid":2,"msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":null}}}`,
				`{"pid":1,"msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":50}}}`,
				`{"ts":1790000000,"pid":1,"msg":"auth init user_info check","ctx":{"user_id":"user-z"}}`,
			}},
			account: "user-a",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Grok(grokHome(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			if r.Account != tc.account {
				t.Errorf("account = %q, want %q", r.Account, tc.account)
			}
			switch {
			case tc.percent == 0 && (r.Quota != nil || r.Plan != ""):
				t.Errorf("quota = %v, plan %q; want none", describe(r.Quota), r.Plan)
			case tc.percent != 0 && (r.Quota == nil || r.Quota.Windows[0].Percent != tc.percent):
				t.Errorf("quota = %v, want %g%%", describe(r.Quota), tc.percent)
			}
		})
	}
}

func TestGrokWindowNaming(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   snapshot.Window
	}{
		// TestGrokReading has a weekly period.
		{
			"monthly",
			`,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","start":"2026-09-01T00:00:00+00:00","end":"2026-10-01T00:00:00+00:00"}`,
			snapshot.Window{Name: "month credits", Minutes: 30 * 1440, ResetsAt: ts("2026-10-01T00:00:00Z")},
		},
		{
			"other period named by length",
			`,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_DAILY","start":"2026-09-22T00:00:00.4Z","end":"2026-09-23T00:00:00Z"}`,
			snapshot.Window{Name: "1d credits", Minutes: 1440, ResetsAt: ts("2026-09-23T00:00:00Z")},
		},
		{
			"billing period when there is no current period",
			`,"billingPeriodStart":"2026-09-20T00:00:00Z","billingPeriodEnd":"2026-09-20T05:00:00Z"`,
			snapshot.Window{Name: "5h credits", Minutes: 300, ResetsAt: ts("2026-09-20T05:00:00Z")},
		},
		{
			"end before start",
			`,"currentPeriod":{"type":"","start":"2026-09-23T00:00:00Z","end":"2026-09-22T00:00:00Z"}`,
			snapshot.Window{Name: "credits", ResetsAt: ts("2026-09-22T00:00:00Z")},
		},
		{"no period", ``, snapshot.Window{Name: "credits"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := grokHome(t, map[string][]string{"unified.jsonl": {
				authLine("2026-09-22T10:00:00Z", 1, "user-a"),
				billLine("2026-09-22T10:01:00Z", 1, 12.5, tc.config),
			}})
			r, err := Grok(home)
			if err != nil {
				t.Fatal(err)
			}
			tc.want.Percent = 12.5
			if r.Quota == nil || !reflect.DeepEqual(r.Quota.Windows, []snapshot.Window{tc.want}) {
				t.Errorf("got %v, want %+v", describe(r.Quota), tc.want)
			}
		})
	}
}

func TestGrokLongLines(t *testing.T) {
	// A line longer than the kept limit must not end the scan, and a line
	// longer than the read buffer is still a whole line: the newest sign-in
	// is such a line, after one too long to keep.
	huge := `{"msg":"tool output","ctx":{"text":"` + strings.Repeat("x", maxGrokLine+10) + `"}}`
	padded := strings.Replace(authLine("2026-09-22T10:00:00Z", 2, "user-b"), `"key_prefix":"xai-"`, `"pad":"`+strings.Repeat("y", 200<<10)+`"`, 1)
	home := grokHome(t, map[string][]string{"unified.jsonl": {
		authLine("2026-09-22T09:00:00Z", 1, "user-a"),
		huge,
		padded,
		billLine("2026-09-22T10:01:00Z", 2, 7, weekly),
	}})
	r, err := Grok(home)
	if err != nil || r.Account != "user-b" || r.Quota == nil || r.Quota.Windows[0].Percent != 7 {
		t.Fatalf("reading = %+v %v, %v", r, describe(r.Quota), err)
	}
}

func TestGrokLastLineWithoutNewline(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "logs", "unified.jsonl"), authLine("2026-09-22T10:00:00Z", 1, "user-a"))
	if r, err := Grok(home); err != nil || r.Account != "user-a" {
		t.Errorf("reading = %+v, %v", r, err)
	}
}

func TestGrokErrors(t *testing.T) {
	if r, err := Grok(t.TempDir()); err == nil || r.Account != "" {
		t.Errorf("no log: %+v, %v", r, err)
	}
	home := grokHome(t, map[string][]string{"unified.jsonl": {billLine("2026-09-22T10:01:00Z", 1, 12, weekly)}})
	r, noSignIn := Grok(home)
	if noSignIn == nil || r.Account != "" || r.Quota != nil {
		t.Errorf("no sign-in: %+v, %v", r, noSignIn)
	}
	// An unreadable log is reported when nothing else named a user.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "logs", "unified.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, err := Grok(home); err == nil || errText(err) == errText(noSignIn) || r.Account != "" {
		t.Errorf("unreadable: %+v, %v", r, err)
	}
}

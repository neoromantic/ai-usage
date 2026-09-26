package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Grok reads the harness's own log, <home>/logs/unified.jsonl. Grok has no
// one-shot command that prints the account or the quota, but each start logs
// the signed-in user id, and each billing refresh logs the credit usage for
// the current period. Only those two kinds of line are decoded, and only the
// user id and the credit fields are kept.
func Grok(home string) (Reading, error) {
	files, _ := filepath.Glob(filepath.Join(home, "logs", "unified*.jsonl"))
	if len(files) == 0 {
		return Reading{}, errors.New("grok log not found; account unknown")
	}
	sort.Strings(files)

	var st grokScan
	var readErr error
	for _, f := range files {
		if err := st.scan(f); err != nil {
			readErr = err
		}
	}
	r := Reading{Account: st.newestUser(func(grokAuth) bool { return true })}
	if r.Account == "" {
		if readErr != nil {
			return r, errors.New("grok log: " + shortErr(readErr))
		}
		return r, errors.New("grok log names no signed-in user; account unknown")
	}
	if b := st.billingFor(r.Account); b != nil {
		r.Plan = b.plan
		r.Quota = b.quota
	}
	return r, nil
}

type grokAuth struct {
	at   time.Time
	pid  int
	user string
}

type grokBilling struct {
	pid   int
	plan  string
	quota *Quota
}

// grokScan keeps both kinds of line from every file and pairs them only
// after all files are read, so rotated files can come in any order.
type grokScan struct {
	auths    []grokAuth
	billings []*grokBilling
}

type grokLogLine struct {
	TS  time.Time       `json:"ts"`
	PID int             `json:"pid"`
	Msg string          `json:"msg"`
	Ctx json.RawMessage `json:"ctx"`
}

// maxGrokLine bounds the lines kept. The two kinds decoded are far shorter;
// a longer line is skipped instead of ending the scan before the newest ones.
const maxGrokLine = 1 << 20

func (s *grokScan) scan(path string) error {
	_, err := logs.ForEachLine(path, maxGrokLine, s.add)
	return err
}

func (s *grokScan) add(line []byte) {
	isUser := bytes.Contains(line, []byte(`"auth init user_info check"`))
	isBilling := bytes.Contains(line, []byte(`"billing: fetched credits config"`))
	if !isUser && !isBilling {
		return
	}
	var row grokLogLine
	if json.Unmarshal(line, &row) != nil {
		return
	}
	switch row.Msg {
	case "auth init user_info check":
		var c struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(row.Ctx, &c) != nil || c.UserID == "" {
			return
		}
		s.auths = append(s.auths, grokAuth{at: row.TS.UTC(), pid: row.PID, user: c.UserID})
	case "billing: fetched credits config":
		if b := grokBillingLine(row); b != nil {
			s.billings = append(s.billings, b)
		}
	}
}

// newestUser is the user of the newest sign-in line that keep accepts.
func (s *grokScan) newestUser(keep func(grokAuth) bool) string {
	var best *grokAuth
	for i := range s.auths {
		if a := &s.auths[i]; keep(*a) && (best == nil || !a.at.Before(best.at)) {
			best = a
		}
	}
	if best == nil {
		return ""
	}
	return best.user
}

// billingFor is the newest billing line not known to belong to someone else.
// Another user's process can refresh billing after this user signed in; its
// line must not hide this user's older one. A line whose process has no
// sign-in left in the log is taken as this user's.
func (s *grokScan) billingFor(user string) *grokBilling {
	var best *grokBilling
	for _, b := range s.billings {
		if owner := s.ownerOf(b); owner != "" && owner != user {
			continue
		}
		if best == nil || !b.quota.At.Before(best.quota.At) {
			best = b
		}
	}
	return best
}

// ownerOf is the user the same process signed in as last before the billing
// line. Pids are reused, so a sign-in after the line does not count.
func (s *grokScan) ownerOf(b *grokBilling) string {
	return s.newestUser(func(a grokAuth) bool {
		return a.pid == b.pid && !a.at.After(b.quota.At)
	})
}

func grokBillingLine(row grokLogLine) *grokBilling {
	var c struct {
		Config struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type  string `json:"type"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"currentPeriod"`
			BillingPeriodStart string `json:"billingPeriodStart"`
			BillingPeriodEnd   string `json:"billingPeriodEnd"`
		} `json:"config"`
		SubscriptionTier string `json:"subscriptionTier"`
	}
	if json.Unmarshal(row.Ctx, &c) != nil || c.Config.CreditUsagePercent == nil || row.TS.IsZero() {
		return nil
	}
	start, end := c.Config.BillingPeriodStart, c.Config.BillingPeriodEnd
	kind := ""
	if p := c.Config.CurrentPeriod; p != nil {
		start, end, kind = p.Start, p.End, p.Type
	}
	w := snapshot.Window{Percent: *c.Config.CreditUsagePercent, ResetsAt: parseTime(end)}
	if s, e := parseTime(start), w.ResetsAt; s != nil && e != nil && e.After(*s) {
		w.Minutes = int(e.Sub(*s).Round(time.Hour).Minutes())
	}
	switch {
	case strings.Contains(kind, "WEEKLY"):
		w.Name = "7d credits"
	case strings.Contains(kind, "MONTHLY"):
		w.Name = "month credits"
	case w.Minutes > 0:
		w.Name = snapshot.DurationName(w.Minutes) + " credits"
	default:
		w.Name = "credits"
	}
	return &grokBilling{
		pid:   row.PID,
		plan:  c.SubscriptionTier,
		quota: &Quota{At: row.TS.UTC(), Source: "log", Windows: []snapshot.Window{w}},
	}
}

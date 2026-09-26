package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Each item takes the first status that applies, and its time is the one
// the status is about.
func TestHealth(t *testing.T) {
	ran, pushed, checked := now.Add(-7*time.Minute), now.Add(-6*time.Minute), now.Add(-2*time.Hour)
	ok := func() Collector {
		return Collector{
			Version: "v1.2.3", LastRunAt: tp(ran), LastSuccessAt: tp(ran),
			Relay:    Relay{URL: strPtr("https://relay.example.com"), LastPushAt: tp(pushed)},
			Schedule: Schedule{Registered: true},
			Update:   Update{CheckedAt: tp(checked), Latest: strPtr("v1.2.3")},
		}
	}
	// item status state at release
	str := func(h Health) string {
		at, rel := "-", "-"
		if h.At != nil {
			at = h.At.Format("15:04")
		}
		if h.Release != nil {
			rel = *h.Release
		}
		return strings.Join([]string{h.Item, h.Status, h.State, at, rel}, " ")
	}
	for _, c := range []struct {
		name   string
		change func(*Collector)
		want   []string
	}{
		{"all well", func(*Collector) {}, []string{"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update ok ok 10:00 -"}},
		{"nothing yet", func(c *Collector) { *c = Collector{Version: "v1.2.3"} }, []string{
			"collection never error - -", "relay none off - -", "update unchecked off - -"}},
		{"run failed", func(c *Collector) {
			c.LastRunAt, c.LastError, c.LastErrorAt = tp(now), strPtr("boom"), tp(now)
		}, []string{"collection failed error 12:00 -", "relay ok ok 11:54 -", "update ok ok 10:00 -"}},
		{"not scheduled", func(c *Collector) { c.Schedule.Registered = false }, []string{
			"collection unscheduled warn 11:53 -", "relay ok ok 11:54 -", "update ok ok 10:00 -"}},
		{"relay failing", func(c *Collector) { c.Relay.LastError, c.Relay.Pending = strPtr("HTTP 503"), true }, []string{
			"collection ok ok 11:53 -", "relay failing error 11:54 -", "update ok ok 10:00 -"}},
		{"relay pending", func(c *Collector) { c.Relay.Pending = true }, []string{
			"collection ok ok 11:53 -", "relay pending warn 11:54 -", "update ok ok 10:00 -"}},
		{"never pushed", func(c *Collector) { c.Relay.LastPushAt = nil }, []string{
			"collection ok ok 11:53 -", "relay pending warn - -", "update ok ok 10:00 -"}},
		{"no relay", func(c *Collector) { c.Relay.URL = nil }, []string{
			"collection ok ok 11:53 -", "relay none off - -", "update ok ok 10:00 -"}},
		{"dev build", func(c *Collector) { c.Version, c.Update.Error = "dev", strPtr("x") }, []string{
			"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update dev off 10:00 -"}},
		{"staged", func(c *Collector) { c.Update.Staged, c.Update.Error = strPtr("v1.3.0"), strPtr("x") }, []string{
			"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update staged info 10:00 v1.3.0"}},
		{"update failed", func(c *Collector) { c.Update.Error, c.Update.Latest = strPtr("x"), strPtr("v1.3.0") }, []string{
			"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update failed error 10:00 -"}},
		{"not checked", func(c *Collector) { c.Update = Update{} }, []string{
			"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update unchecked off - -"}},
		{"available", func(c *Collector) { c.Update.Latest = strPtr("v1.3.0") }, []string{
			"collection ok ok 11:53 -", "relay ok ok 11:54 -", "update available warn 10:00 v1.3.0"}},
	} {
		col := ok()
		c.change(&col)
		var got []string
		for _, h := range health(col) {
			got = append(got, str(h))
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

// The header shows the health the report has, and collecting in place of
// the collection while the page collects.
func TestHeaderShowsTheReportsHealth(t *testing.T) {
	r := Report{GeneratedAt: now}
	r.Collector = Collector{DeviceLabel: "box", Version: "v1.2.3", LastRunAt: tp(now), LastSuccessAt: tp(now), Schedule: Schedule{Registered: true}}
	r.Collector.Health = health(r.Collector)
	for _, c := range []struct {
		busy string
		want string
	}{
		{"", "● collected just now  ● no relay  ● update not checked"},
		{"|", "| collecting  ● no relay  ● update not checked"},
	} {
		h := sgr.ReplaceAllString(Render(r, Options{Width: 100, Loc: time.UTC, Busy: c.busy}).Header, "")
		if !strings.HasSuffix(h, c.want) {
			t.Errorf("busy %q: header %q, want it to end in %q", c.busy, h, c.want)
		}
	}
}

// A report saved before it had health and limits gets them from Fill as
// Build made them, and a report that has them keeps them.
func TestFillGivesAnEarlierReportHealthAndLimits(t *testing.T) {
	for _, name := range []string{"team", "single"} {
		saved := loadReport(t, name)
		want, _ := json.Marshal(saved)
		Fill(&saved)
		if got, _ := json.Marshal(saved); string(got) != string(want) {
			t.Errorf("%s: Fill changed a report that has health and limits", name)
		}

		earlier := loadReport(t, name)
		earlier.Collector.Health = nil
		more := 0
		for _, q := range quotas(&earlier) {
			for i := range q.Windows {
				if q.Windows[i].Limits && !q.Windows[i].Main {
					more++
				}
				q.Windows[i].Limits = false
			}
		}
		if more == 0 {
			t.Fatalf("%s: no window limits its account more than the main one", name)
		}
		Fill(&earlier)
		if got, _ := json.Marshal(earlier); string(got) != string(want) {
			t.Errorf("%s: the filled report differs from the saved one", name)
		}
	}
}

// quotas are the quotas of a report's accounts, this device's and the
// team's.
func quotas(r *Report) []*Quota {
	var qs []*Quota
	for _, p := range r.Providers {
		for _, a := range p.Accounts {
			if a.Quota != nil {
				qs = append(qs, a.Quota)
			}
		}
	}
	for _, p := range r.Team.Providers {
		for _, a := range p.Accounts {
			if a.Quota != nil {
				qs = append(qs, a.Quota)
			}
		}
	}
	return qs
}

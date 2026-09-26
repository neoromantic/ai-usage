package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/tui"
	"github.com/neoromantic/ai-usage/internal/view"
)

// loadResult rebuilds a run result from disk without collecting.
func loadResult(d state.Dir) (*collect.Result, error) {
	cfg, err := d.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, err := collect.LoadKey(d)
	if err != nil {
		return nil, err
	}
	st, err := d.LoadState()
	if err != nil {
		return nil, err
	}
	cache, _ := collect.LoadTeamCache(d)
	doc := collect.BuildDoc(st, key, cfg, deviceName(cfg), osUser(), version, st.LastRunAt)
	return &collect.Result{Config: cfg, State: st, Key: key, Doc: doc, Team: cache}, nil
}

// reportAt is the report of a run result as of now.
func reportAt(d state.Dir, res *collect.Result, now time.Time) view.Report {
	return view.Build(view.Input{
		Version:  version,
		RelayURL: relayURL(res.Config),
		Config:   res.Config,
		State:    liveSchedule(d, res.State),
		Key:      res.Key,
		Doc:      res.Doc,
		Team:     res.Team,
		Hostname: deviceName(res.Config),
		OSUser:   osUser(),
		Now:      now,
	})
}

func printReport(stdout io.Writer, r view.Report, jsonOut, guide bool, disp *display) error {
	if jsonOut {
		return writeJSON(stdout, r)
	}
	o := disp.options(stdout, true)
	text := view.Text(r, o)
	if guide {
		text += "\n" + view.Guide(r, newScheduler().Name(), o)
	}
	_, err := io.WriteString(disp.writer(stdout), text)
	return err
}

func cmdReport(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flags("report")
	jsonOut := fs.Bool("json", false, "")
	from := fs.String("from", "", "")
	disp := displayFlags(fs, true)
	if err := disp.parse(fs, args); err != nil {
		return err
	}
	if *from != "" {
		return reportFrom(ctx, *from, *jsonOut, disp, stdout)
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	// Checked before loadResult, which would create a team key and a device id.
	if st, err := d.LoadState(); err != nil {
		return err
	} else if st.LastRunAt.IsZero() {
		return errors.New("nothing collected yet; run `ai-usage` first")
	}
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	if disp.interactive(stdout, *jsonOut, false) {
		return tui.Run(ctx, viewConfig(d, res, disp, false, stdout), os.Stdin, stdout)
	}
	return printReport(stdout, reportAt(d, res, clock().UTC()), *jsonOut, false, disp)
}

// reportFrom shows a report saved with --json, such as a teammate's or a
// demo's, rather than this device's. It reads no state and collects
// nothing, so the interactive view has no `r`.
func reportFrom(ctx context.Context, path string, jsonOut bool, disp *display, stdout io.Writer) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var r view.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if r.SchemaVersion != view.SchemaVersion {
		return fmt.Errorf("%s: schema_version %d, this release reads %d", path, r.SchemaVersion, view.SchemaVersion)
	}
	if disp.interactive(stdout, jsonOut, false) {
		return tui.Run(ctx, disp.tuiConfig(stdout, r), os.Stdin, stdout)
	}
	return printReport(stdout, r, jsonOut, false, disp)
}

func cmdStatus(args []string, stdout io.Writer) error {
	fs := flags("status")
	jsonOut := fs.Bool("json", false, "")
	disp := displayFlags(fs, false)
	if err := disp.parse(fs, args); err != nil {
		return err
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	r := reportAt(d, res, clock().UTC())
	type source struct {
		Provider string   `json:"provider"`
		Status   string   `json:"status"`
		Error    *string  `json:"error"`
		Homes    []string `json:"homes"`
	}
	out := struct {
		SchemaVersion int            `json:"schema_version"`
		Collector     view.Collector `json:"collector"`
		Sources       []source       `json:"sources"`
	}{SchemaVersion: view.SchemaVersion, Collector: r.Collector}
	for _, p := range r.Providers {
		out.Sources = append(out.Sources, source{p.Provider, p.Status, p.Error, p.Homes})
	}
	if *jsonOut {
		return writeJSON(stdout, out)
	}
	_, err = io.WriteString(disp.writer(stdout), view.StatusText(r, string(d), disp.options(stdout, true)))
	return err
}

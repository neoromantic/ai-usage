package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/state"
)

// cmdHome lists, adds, and removes the harness homes this device reads
// besides the ones it finds itself.
func cmdHome(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	d, err := dir()
	if err != nil {
		return err
	}
	userHome, _ := os.UserHomeDir()
	switch sub {
	case "", "list":
		if len(args) > 0 {
			return usageError("home takes no arguments")
		}
		cfg, err := d.LoadConfig()
		if err != nil {
			return err
		}
		return listHomes(cfg, userHome, stdout)
	case "add", "remove":
	default:
		return usageError("unknown home command " + sub)
	}

	var quotaFrom []string
	var rest []string
	forget := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--forget" || a == "-forget":
			if sub != "remove" {
				return usageError("--forget is for home remove")
			}
			forget = true
		case a == "--quota-from" || a == "-quota-from":
			if i+1 == len(args) {
				return usageError("--quota-from takes PROVIDER:DIR")
			}
			i++
			quotaFrom = append(quotaFrom, args[i])
		case strings.HasPrefix(a, "--quota-from=") || strings.HasPrefix(a, "-quota-from="):
			quotaFrom = append(quotaFrom, a[strings.Index(a, "=")+1:])
		case strings.HasPrefix(a, "-"):
			return usageError("unknown flag " + a)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 2 {
		return usageError("home " + sub + " takes a provider and at least one directory")
	}
	p := rest[0]
	if !known(p) {
		return usageError("unknown provider " + p + "; one of " + strings.Join(collect.Providers, ", "))
	}
	var homes []string
	for _, h := range rest[1:] {
		abs, err := homePath(h, sub == "add")
		if err != nil {
			return err
		}
		homes = append(homes, abs)
	}
	// One --quota-from per harness: codex:DIR, grok:DIR, or both.
	var refs []homeRef
	for _, q := range quotaFrom {
		if sub != "add" || p != "hermes" {
			return usageError("--quota-from is for home add hermes")
		}
		qp, qdir, ok := strings.Cut(q, ":")
		if !ok || !collect.BillsThrough(qp) || strings.TrimSpace(qdir) == "" {
			return usageError("--quota-from takes codex:DIR or grok:DIR")
		}
		for _, r := range refs {
			if r.provider == qp {
				return usageError("--quota-from names " + qp + " twice")
			}
		}
		abs, err := homePath(qdir, true)
		if err != nil {
			return err
		}
		refs = append(refs, homeRef{provider: qp, home: abs})
	}

	// A home whose sessions cannot be read changes nothing. They are read
	// again under the run lock, once no run reads the home any more.
	if forget {
		if err := collect.CheckReadable(p, homes, clock()); err != nil {
			return err
		}
	}
	var kept []string

	// The config has a lock of its own, so this never waits for a
	// collection, and it is released before anything is printed: a process
	// killed while writing to a closed pipe would leave it held.
	cfg, err := d.EditConfig(func(cfg *state.Config) error {
		if sub == "add" {
			addHomes(cfg, userHome, p, homes, refs)
			return nil
		}
		if err := removeHomes(cfg, userHome, p, homes, forget); err != nil || !forget {
			return err
		}
		// Forgetting a home that runs still find would only count its
		// sessions again from nothing.
		kept = collect.Discover(userHome, os.Getenv, cfg.Homes)[p]
		for _, h := range homes {
			for _, k := range kept {
				if samePath(h, k) {
					return fmt.Errorf("%s is still read by every run: it is inside a home that stays, a home an app keeps per account or per session, or one an environment variable names", h)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if forget {
		n, err := collect.Forget(ctx, d, p, homes, kept, clock(), func() (func(), error) {
			return waitLock(ctx, d, stderr)
		})
		if err != nil {
			return fmt.Errorf("the homes were removed, but their sessions were not forgotten, which the same command can try again: %w", err)
		}
		word := "sessions"
		if n == 1 {
			word = "session"
		}
		fmt.Fprintf(stdout, "forgot %d %s counted from these homes; the team sees the change after the next collection\n\n", n, word)
	}
	return listHomes(cfg, userHome, stdout)
}

func known(p string) bool {
	for _, v := range collect.Providers {
		if v == p {
			return true
		}
	}
	return false
}

// homeRef is one harness home.
type homeRef struct{ provider, home string }

// homePath makes h absolute. A home being added must be a directory.
func homePath(h string, mustExist bool) (string, error) {
	// An empty argument, as from an unset shell variable, would otherwise be
	// the working directory.
	if strings.TrimSpace(h) == "" {
		return "", usageError("a directory is empty")
	}
	if strings.HasPrefix(h, "~"+string(filepath.Separator)) || h == "~" {
		if u, err := os.UserHomeDir(); err == nil {
			h = filepath.Join(u, h[1:])
		}
	}
	abs, err := filepath.Abs(h)
	if err != nil {
		return "", err
	}
	if mustExist {
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New(abs + " is not a directory")
		}
	}
	return filepath.Clean(abs), nil
}

func addHomes(cfg *state.Config, userHome, p string, homes []string, refs []homeRef) {
	add := func(p, h string) {
		if h == filepath.Join(userHome, "."+p) || slices.Contains(cfg.Homes[p], h) {
			return
		}
		if cfg.Homes == nil {
			cfg.Homes = map[string][]string{}
		}
		cfg.Homes[p] = append(cfg.Homes[p], h)
		sort.Strings(cfg.Homes[p])
	}
	for _, h := range homes {
		add(p, h)
		for _, ref := range refs {
			if cfg.QuotaFrom == nil {
				cfg.QuotaFrom = map[string]map[string]string{}
			}
			if cfg.QuotaFrom[h] == nil {
				cfg.QuotaFrom[h] = map[string]string{}
			}
			cfg.QuotaFrom[h][ref.provider] = ref.home
		}
	}
	// The login is read where it lives, so that home is read too.
	for _, ref := range refs {
		add(ref.provider, ref.home)
	}
}

// removeHomes takes homes out of the config. With forget, a home no longer
// in it is let be, so that forgetting its sessions can be tried again.
func removeHomes(cfg *state.Config, userHome, p string, homes []string, forget bool) error {
	for _, h := range homes {
		named := p == "hermes" && cfg.QuotaFrom[h] != nil
		if h == filepath.Join(userHome, "."+p) && (!named || forget) {
			return errors.New(h + " is the default " + p + " home, which is always read")
		}
		if !slices.Contains(cfg.Homes[p], h) && !named && !forget {
			return errors.New(h + " is not an added " + p + " home")
		}
		// Hermes homes would quietly fall back to the default login.
		if users := quotaUsers(cfg, p, h); len(users) > 0 {
			return fmt.Errorf("%s is where %d hermes homes take their quota from, such as %s; remove them first, or add them again with another --quota-from", h, len(users), users[0])
		}
	}
	for _, h := range homes {
		var keep []string
		for _, v := range cfg.Homes[p] {
			if v != h {
				keep = append(keep, v)
			}
		}
		if len(keep) == 0 {
			delete(cfg.Homes, p)
		} else {
			cfg.Homes[p] = keep
		}
		if p == "hermes" {
			delete(cfg.QuotaFrom, h)
		}
	}
	return nil
}

// quotaUsers lists the Hermes homes that take their quota from home h of p.
func quotaUsers(cfg *state.Config, p, h string) []string {
	var out []string
	for hermes, refs := range cfg.QuotaFrom {
		if refs[p] == h {
			out = append(out, hermes)
		}
	}
	sort.Strings(out)
	return out
}

// listHomes prints every home a run would read, and what each bills through.
func listHomes(cfg state.Config, userHome string, stdout io.Writer) error {
	found := collect.Discover(userHome, os.Getenv, cfg.Homes)
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, p := range collect.Providers {
		if len(found[p]) == 0 {
			fmt.Fprintf(tw, "%s\t(none)\t\n", p)
			continue
		}
		for i, h := range found[p] {
			name := p
			if i > 0 {
				name = ""
			}
			var notes []string
			if slices.Contains(cfg.Homes[p], h) {
				notes = append(notes, "added")
			}
			if p == "hermes" {
				notes = append(notes, quotaNotes(collect.QuotaHomesOf(cfg.QuotaFrom, h), userHome)...)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", name, tilde(h, userHome), strings.Join(notes, " · "))
		}
	}
	// A home added or named that is gone is not read; say so rather than
	// drop it.
	for _, p := range collect.Providers {
		var missing []string
		gone := map[string]bool{}
		for _, h := range cfg.Homes[p] {
			if !slices.Contains(found[p], h) {
				gone[h] = true
			}
		}
		if p == "hermes" {
			for h := range cfg.QuotaFrom {
				if !slices.Contains(found[p], h) {
					gone[h] = true
				}
			}
		}
		for h := range gone {
			notes := []string{"missing"}
			if p == "hermes" {
				notes = append(notes, quotaNotes(cfg.QuotaFrom[h], userHome)...)
			}
			missing = append(missing, tilde(h, userHome)+"\t"+strings.Join(notes, " · "))
		}
		sort.Strings(missing)
		for _, m := range missing {
			fmt.Fprintf(tw, "%s\t%s\n", p, m)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line != "" {
			fmt.Fprintln(stdout, strings.TrimRight(line, " \n"))
		}
	}
	return nil
}

// quotaNotes says, by harness, where a Hermes home takes its quota from.
func quotaNotes(refs map[string]string, userHome string) []string {
	var out []string
	for _, p := range collect.Providers {
		if at := refs[p]; at != "" {
			out = append(out, "quota from "+p+" "+tilde(at, userHome))
		}
	}
	return out
}

func tilde(p, userHome string) string {
	if userHome != "" && (p == userHome || strings.HasPrefix(p, userHome+string(filepath.Separator))) {
		return "~" + p[len(userHome):]
	}
	return p
}

// samePath reports whether a and b are one folder, links resolved.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

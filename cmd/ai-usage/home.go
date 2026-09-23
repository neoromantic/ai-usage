package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/state"
)

// cmdHome lists, adds, and removes the harness homes this device reads
// besides the ones it finds itself.
func cmdHome(args []string, stdout io.Writer) error {
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

	var quotaFrom string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--quota-from" || a == "-quota-from":
			if i+1 == len(args) {
				return usageError("--quota-from takes PROVIDER:DIR")
			}
			i++
			quotaFrom = args[i]
		case strings.HasPrefix(a, "--quota-from=") || strings.HasPrefix(a, "-quota-from="):
			quotaFrom = a[strings.Index(a, "=")+1:]
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
	var ref *state.HomeRef
	if quotaFrom != "" {
		if sub != "add" || p != "hermes" {
			return usageError("--quota-from is for home add hermes")
		}
		qp, qdir, ok := strings.Cut(quotaFrom, ":")
		if !ok || !collect.BillsThrough(qp) {
			return usageError("--quota-from takes codex:DIR or grok:DIR")
		}
		abs, err := homePath(qdir, true)
		if err != nil {
			return err
		}
		ref = &state.HomeRef{Provider: qp, Home: abs}
	}

	cfg, err := changeHomes(d, func(cfg *state.Config) error {
		if sub == "add" {
			addHomes(cfg, userHome, p, homes, ref)
			return nil
		}
		return removeHomes(cfg, userHome, p, homes)
	})
	if err != nil {
		return err
	}
	return listHomes(cfg, userHome, stdout)
}

// changeHomes edits the config under the run lock, since a scheduled run
// rewrites it too. The lock is released before anything is printed: a
// process killed while writing to a closed pipe would leave it held.
func changeHomes(d state.Dir, edit func(*state.Config) error) (state.Config, error) {
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		return state.Config{}, err
	}
	defer unlock()
	cfg, err := d.LoadConfig()
	if err != nil {
		return cfg, err
	}
	if err := edit(&cfg); err != nil {
		return cfg, err
	}
	return cfg, d.SaveConfig(cfg)
}

func known(p string) bool {
	for _, v := range collect.Providers {
		if v == p {
			return true
		}
	}
	return false
}

// homePath makes h absolute. A home being added must be a directory.
func homePath(h string, mustExist bool) (string, error) {
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

func addHomes(cfg *state.Config, userHome, p string, homes []string, ref *state.HomeRef) {
	add := func(p, h string) {
		if h == filepath.Join(userHome, "."+p) || contains(cfg.Homes[p], h) {
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
		if ref != nil {
			if cfg.QuotaFrom == nil {
				cfg.QuotaFrom = map[string]state.HomeRef{}
			}
			cfg.QuotaFrom[h] = *ref
		}
	}
	// The login is read where it lives, so that home is read too.
	if ref != nil {
		add(ref.Provider, ref.Home)
	}
}

func removeHomes(cfg *state.Config, userHome, p string, homes []string) error {
	for _, h := range homes {
		if h == filepath.Join(userHome, "."+p) {
			return errors.New(h + " is the default " + p + " home, which is always read")
		}
		_, named := cfg.QuotaFrom[h]
		if !contains(cfg.Homes[p], h) && !named {
			return errors.New(h + " is not an added " + p + " home")
		}
	}
	for _, h := range homes {
		var keep []string
		for _, v := range cfg.Homes[p] {
			if v != h {
				keep = append(keep, v)
			}
		}
		cfg.Homes[p] = keep
		if len(keep) == 0 {
			delete(cfg.Homes, p)
		}
		if p == "hermes" {
			delete(cfg.QuotaFrom, h)
		}
	}
	return nil
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
			if contains(cfg.Homes[p], h) {
				notes = append(notes, "added")
			}
			if p == "hermes" {
				if ref, ok := collect.QuotaHomeOf(cfg.QuotaFrom, h); ok {
					notes = append(notes, "quota from "+ref.Provider+" "+tilde(ref.Home, userHome))
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", name, tilde(h, userHome), strings.Join(notes, " · "))
		}
	}
	// A named home that is gone is not read; say so rather than drop it.
	var missing []string
	for h, ref := range cfg.QuotaFrom {
		if !contains(found["hermes"], h) {
			missing = append(missing, tilde(h, userHome)+" (quota from "+ref.Provider+" "+tilde(ref.Home, userHome)+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		fmt.Fprintf(tw, "hermes\t%s\tmissing\n", m)
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

func tilde(p, userHome string) string {
	if userHome != "" && (p == userHome || strings.HasPrefix(p, userHome+string(filepath.Separator))) {
		return "~" + p[len(userHome):]
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

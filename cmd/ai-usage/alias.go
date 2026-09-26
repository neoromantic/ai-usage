package main

import (
	"bytes"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
)

// argError is an argument that names nothing, or more than one thing, this
// device knows. It exits 2 like a usage error, but says what there is instead
// of printing the usage.
type argError string

func (e argError) Error() string { return string(e) }

// cmdAlias lists the short names the team gave accounts, or gives an account
// one. The name travels sealed in this device's snapshot, and the newest name
// for an account, from any device, is the one the team sees.
func cmdAlias(args []string, stdout io.Writer) error {
	var rest []string
	clear := false
	for _, a := range args {
		switch {
		case a == "--clear" || a == "-clear":
			clear = true
		case a == "-h" || a == "-help" || a == "--help":
			return flag.ErrHelp
		case strings.HasPrefix(a, "-"):
			return usageError("unknown flag " + a)
		default:
			rest = append(rest, a)
		}
	}
	switch {
	case len(rest) == 0 && !clear:
	case clear && len(rest) != 1:
		return usageError("alias --clear takes one account")
	case !clear && len(rest) != 2:
		return usageError("alias takes an account and a name, or an account and --clear")
	}
	var name string
	if !clear && len(rest) == 2 {
		name = strings.TrimSpace(rest[1])
		if err := snapshot.CheckAlias(name); err != nil {
			return usageError(err.Error())
		}
	}
	d, err := dir()
	if err != nil {
		return err
	}
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	b := loadAliasBook(d, cfg, st)
	if len(rest) == 0 {
		return b.list(stdout)
	}
	targets, err := b.match(rest[0])
	if err != nil {
		return err
	}
	if !clear {
		if err := b.free(targets, name); err != nil {
			return err
		}
	}
	was := b.nameOf(targets)
	now := clock().UTC()
	if _, err := d.EditConfig(func(c *state.Config) error {
		if c.Aliases == nil {
			c.Aliases = map[string]state.Alias{}
		}
		for _, t := range targets {
			c.Aliases[state.Key(t.provider, t.label)] = state.Alias{Name: name, At: now}
		}
		return nil
	}); err != nil {
		return err
	}
	label, on := snapshot.Printable(targets[0].label), providersOf(targets)
	same := ""
	if len(targets) > 1 {
		same = ", the same label on each and so one person"
	}
	switch {
	case !clear:
		fmt.Fprintf(stdout, "%s is now %s on %s%s; the team sees the name after the next run\n", label, name, on, same)
	case was != "":
		fmt.Fprintf(stdout, "%s no longer goes by %s on %s%s; the team sees the change after the next run\n", label, was, on, same)
	default:
		fmt.Fprintf(stdout, "%s has no name on %s%s; the clearing reaches the team with the next run\n", label, on, same)
	}
	if len(targets) > 1 {
		fmt.Fprintf(stdout, "a provider before the account, as in %s:%s, picks one alone\n", targets[len(targets)-1].provider, label)
	}
	return nil
}

// account is one account this device knows of.
type account struct{ provider, label string }

// aliasSet is the newest name an account was given, or its clearing.
type aliasSet struct {
	name   string
	at     time.Time
	device string
	// by is the device that set it, empty for this one.
	by string
}

// aliasBook is every account this device knows, from its ledger and the
// team's last read, with the names the team gave them.
type aliasBook struct {
	accounts []account
	names    map[string]aliasSet
}

func loadAliasBook(d state.Dir, cfg state.Config, st *state.State) *aliasBook {
	b := &aliasBook{names: map[string]aliasSet{}}
	seen := map[string]bool{}
	add := func(p, l string) {
		k := state.Key(p, l)
		if l == "" || l == collect.UnknownAccount || !snapshot.KnownProvider(p) || seen[k] {
			return
		}
		seen[k] = true
		b.accounts = append(b.accounts, account{p, l})
	}
	note := func(p, l string, a aliasSet) {
		k := state.Key(p, l)
		cur, ok := b.names[k]
		if !ok || snapshot.AliasWins(a.at, a.device, cur.at, cur.device) {
			b.names[k] = a
		}
	}
	for _, a := range collect.Totals(st) {
		add(a.Provider, a.Label)
	}
	for k, a := range cfg.Aliases {
		parts := state.SplitKey(k)
		if len(parts) != 2 || a.At.IsZero() {
			continue
		}
		add(parts[0], parts[1])
		note(parts[0], parts[1], aliasSet{name: a.Name, at: a.At, device: cfg.Device})
	}
	// The other devices, as the team was last read. This device's own
	// snapshot there may be older than its config.
	if key, err := team.Load(d.KeyFile()); err == nil {
		if cache, err := collect.LoadTeamCache(d); err == nil && cache.Team == key.Fingerprint() {
			open := func(s string) (string, bool) {
				v, err := key.Open(s)
				return snapshot.Printable(v), err == nil
			}
			for _, doc := range cache.Docs {
				if doc.Device == cfg.Device {
					continue
				}
				for _, a := range doc.Accounts {
					if l, ok := open(a.Label); ok {
						add(a.Provider, l)
					}
				}
				by, _ := open(doc.DeviceLabel)
				for _, a := range doc.Aliases {
					l, ok := open(a.Label)
					n, nok := open(a.Name)
					// A name this command would refuse is not one.
					if !ok || !nok || (n != "" && snapshot.CheckAlias(n) != nil) {
						continue
					}
					add(a.Provider, l)
					note(a.Provider, l, aliasSet{name: n, at: a.At, device: doc.Device, by: cmp.Or(by, "unknown")})
				}
			}
		}
	}
	sort.Slice(b.accounts, func(i, j int) bool {
		x, y := b.accounts[i], b.accounts[j]
		if x.provider != y.provider {
			return slices.Index(snapshot.Providers, x.provider) < slices.Index(snapshot.Providers, y.provider)
		}
		return x.label < y.label
	})
	return b
}

// alias is the name the team gave a, or "".
func (b *aliasBook) alias(a account) string { return b.names[state.Key(a.provider, a.label)].name }

// current is the name the report shows for a: its alias, else its short name.
func (b *aliasBook) current(a account) string {
	if n := b.alias(a); n != "" {
		return n
	}
	return view.ShortName(a.label)
}

// nameOf is the name the accounts go by now, when they have one.
func (b *aliasBook) nameOf(as []account) string {
	for _, a := range as {
		if n := b.alias(a); n != "" {
			return n
		}
	}
	return ""
}

// match finds the accounts q names: an exact label, with the same label on
// every provider, else an account's name or short name when it names one
// label. PROVIDER:ACCOUNT picks one provider.
func (b *aliasBook) match(q string) ([]account, error) {
	q = strings.TrimSpace(q)
	provider := ""
	if p, rest, ok := strings.Cut(q, ":"); ok && snapshot.KnownProvider(p) {
		provider, q = p, strings.TrimSpace(rest)
	}
	if q == "" {
		return nil, usageError("an account cannot be empty")
	}
	if len(b.accounts) == 0 {
		return nil, errors.New("this device knows no accounts yet; run `ai-usage` first")
	}
	var pool []account
	for _, a := range b.accounts {
		if provider == "" || a.provider == provider {
			pool = append(pool, a)
		}
	}
	var found []account
	for _, a := range pool {
		if snapshot.LabelKey(a.label) == snapshot.LabelKey(q) {
			found = append(found, a)
		}
	}
	if len(found) == 0 {
		for _, a := range pool {
			if strings.EqualFold(b.alias(a), q) || strings.EqualFold(view.ShortName(a.label), q) {
				found = append(found, a)
			}
		}
	}
	what := q
	if provider != "" {
		what = provider + ":" + q
	}
	if len(found) == 0 {
		if len(pool) == 0 {
			pool = b.accounts
		}
		return nil, argError(fmt.Sprintf("no account this device knows goes by %q; the accounts, with the names they go by:\n%s", what, b.table(pool)))
	}
	people := map[string]bool{}
	for _, a := range found {
		people[snapshot.LabelKey(a.label)] = true
	}
	if len(people) > 1 {
		return nil, argError(fmt.Sprintf("%q names more than one account; give its whole label, or PROVIDER:LABEL for one provider alone\n%s", what, b.table(found)))
	}
	return found, nil
}

// free refuses a name another account of the same provider goes by, since
// the report could not tell the two apart.
func (b *aliasBook) free(targets []account, name string) error {
	for _, t := range targets {
		for _, o := range b.accounts {
			if o.provider == t.provider && snapshot.LabelKey(o.label) != snapshot.LabelKey(t.label) && strings.EqualFold(b.current(o), name) {
				return argError(fmt.Sprintf("%s %s already goes by %s; choose another name", o.provider, snapshot.Printable(o.label), b.current(o)))
			}
		}
	}
	return nil
}

// table lists accounts, one a line, with the name each goes by.
func (b *aliasBook) table(as []account) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, a := range as {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", a.provider, snapshot.Printable(a.label), b.current(a))
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// list prints the names the team gave accounts, and who gave them.
func (b *aliasBook) list(stdout io.Writer) error {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	n := 0
	for _, a := range b.accounts {
		s := b.names[state.Key(a.provider, a.label)]
		if s.name == "" {
			continue
		}
		by := "this device"
		if s.by != "" {
			by = s.by
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\tset on %s %s\n", a.provider, snapshot.Printable(a.label), s.name, by, s.at.Local().Format("2006-01-02 15:04"))
		n++
	}
	if n == 0 {
		_, err := fmt.Fprintln(stdout, "no account has a name yet; `ai-usage alias ACCOUNT NAME` names one for the whole team")
		return err
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := io.Copy(stdout, &buf)
	return err
}

// providersOf names the accounts' providers: claude, or claude and codex.
func providersOf(as []account) string {
	var ps []string
	for _, a := range as {
		if len(ps) == 0 || ps[len(ps)-1] != a.provider {
			ps = append(ps, a.provider)
		}
	}
	if len(ps) == 1 {
		return ps[0]
	}
	return strings.Join(ps[:len(ps)-1], ", ") + " and " + ps[len(ps)-1]
}

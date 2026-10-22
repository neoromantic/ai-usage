package main

import (
	"bufio"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
)

// cmdCost prints what the team's subscriptions cost, or records what a
// person says of one: its price, cadence, and next charge, that it is still
// paid or cancelled, or a question not to ask again. An answer travels
// sealed in this device's snapshot, and the newest one for each field, from
// any device, is the one the team sees.
func cmdCost(args []string, stdout io.Writer) error {
	sub, rest := subcommand(args)
	switch {
	case sub == "-h" || sub == "-help" || sub == "--help":
		return flag.ErrHelp
	case sub == "" || strings.HasPrefix(sub, "-"):
		return costReport(args, stdout)
	case sub == "prices":
		if len(rest) > 0 {
			return usageError("cost prices takes no arguments")
		}
		return costPrices(stdout)
	}
	return costEdit(args, stdout)
}

// costReport prints COST and what each device used, or with --json the
// report's cost object.
func costReport(args []string, stdout io.Writer) error {
	fs := flags("cost")
	jsonOut := fs.Bool("json", false, "")
	disp := displayFlags(fs, false)
	if err := disp.parse(fs, args); err != nil {
		return err
	}
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	if st, err := d.LoadState(); err != nil {
		return err
	} else if st.LastRunAt.IsZero() {
		return errors.New("nothing collected yet; run `ai-usage` first")
	}
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	r := reportAt(d, res, clock().UTC())
	if *jsonOut {
		return writeJSON(stdout, r.Cost)
	}
	_, err = io.WriteString(disp.writer(stdout), view.CostText(r, disp.options(stdout, true)))
	return err
}

// costPrices prints the price list in use, when it was last checked, and
// where it came from.
func costPrices(stdout io.Writer) error {
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	list, from := cost.Current(d)
	fmt.Fprintf(stdout, "price list of %s, %s; prices in %s\n", list.Updated, from, list.Currency)
	rows := [][]string{{"PROVIDER", "PLAN", "NAME", "A MONTH", "A YEAR"}}
	for _, p := range snapshot.Providers {
		for _, pl := range list.Plans {
			if pl.Provider != p {
				continue
			}
			year := "·"
			if pl.AnnualUSD > 0 {
				year = "$" + strconv.FormatFloat(pl.AnnualUSD, 'f', -1, 64)
			}
			rows = append(rows, []string{"  " + p, pl.Plan, pl.DisplayName(), "$" + strconv.FormatFloat(pl.USD, 'f', -1, 64), year})
		}
	}
	rows[0][0] = "  " + rows[0][0]
	_, err = io.WriteString(stdout, tabulate(rows))
	if err == nil {
		_, err = fmt.Fprintln(stdout, "a plan the list lacks has no price until a person types one: ai-usage cost set ACCOUNT --price N")
	}
	return err
}

// costEdit runs an answer, as `cost set`, `confirm`, `cancel`, `reset`,
// `skip`, and `never-ask` take it, and says what it changed in a line.
func costEdit(args []string, stdout io.Writer) error {
	cmd, rest := subcommand(args)
	now := clock()
	switch cmd {
	case "never-ask":
		undo := false
		switch {
		case len(rest) == 1 && (rest[0] == "--undo" || rest[0] == "-undo"):
			undo = true
		case len(rest) > 0:
			return usageError("cost never-ask takes only --undo")
		}
		d, err := state.DefaultDir()
		if err != nil {
			return err
		}
		if _, err := cost.EditStore(d, func(s *cost.Store) error { s.NeverAsk = !undo; return nil }); err != nil {
			return err
		}
		if undo {
			_, err = fmt.Fprintln(stdout, "this device asks about the cost again")
		} else {
			_, err = fmt.Fprintln(stdout, "this device asks nothing about the cost any more; `ai-usage cost never-ask --undo` asks again")
		}
		return err
	case "set", "confirm", "cancel", "reset", "skip":
	default:
		return usageError(fmt.Sprintf("unknown cost command %q", cmd))
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return usageError("cost " + cmd + " takes an account first: a label, a name, or PROVIDER:LABEL")
	}
	query, rest := rest[0], rest[1:]
	e := costChange{cmd: cmd}
	switch cmd {
	case "set":
		var err error
		if e, err = parseSet(rest, now); err != nil {
			return err
		}
	case "skip":
		if len(rest) != 1 || !slices.Contains([]string{cost.AskRenewal, cost.AskPrice, cost.AskStillPaid}, rest[0]) {
			return usageError("cost skip takes an account and a question: renewal, price, or still_paid")
		}
		e.skip = rest[0]
	default:
		if len(rest) > 0 {
			return usageError("cost " + cmd + " takes one account")
		}
	}
	d, cfg, err := loadConfig()
	if err != nil {
		return err
	}
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	store, err := cost.LoadStore(d)
	if err != nil {
		store = nil
	}
	b := loadAliasBook(d, cfg, st)
	if store != nil {
		for _, s := range store.Subs {
			b.addAccount(s.Provider, s.Label)
		}
	}
	targets, err := b.match(query)
	if err != nil {
		return err
	}
	targets = slices.DeleteFunc(targets, func(a account) bool { return a.provider == "hermes" })
	switch {
	case len(targets) == 0:
		return argError("a Hermes account borrows the quota of another login, and costs nothing of its own")
	case len(targets) > 1:
		return argError(fmt.Sprintf("%q names a subscription on %s; give PROVIDER:LABEL for one of them, as in %s:%s",
			query, providersOf(targets), targets[0].provider, snapshot.Printable(targets[0].label)))
	}
	t := targets[0]
	// A price is for the plan the report counts it against: the one the
	// store found newest, else the one this device or the team read.
	plan := planOf(d, st, cfg.Device, t)
	var saved *cost.Sub
	if _, err := cost.EditStore(d, func(s *cost.Store) error {
		saved = s.Sub(t.provider, t.label, now)
		return e.apply(saved, cmp.Or(saved.Plan(), plan), now)
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, e.done(t, saved, now))
	return err
}

// costChange is what a cost command, cmd, says of a subscription.
type costChange struct {
	cmd             string
	price           *float64
	cadence, renews string
	skip            string
}

// parseSet reads `cost set`'s flags after the account: at least one of a
// price, a cadence, and a next charge, as a day or a day of the month.
func parseSet(args []string, now time.Time) (costChange, error) {
	e := costChange{cmd: "set"}
	fs := flags("cost set")
	price := fs.String("price", "", "")
	monthly := fs.Bool("monthly", false, "")
	yearly := fs.Bool("yearly", false, "")
	renews := fs.String("renews", "", "")
	if err := parse(fs, args); err != nil {
		return e, err
	}
	if *price == "" && !*monthly && !*yearly && *renews == "" {
		return e, usageError("cost set takes --price N, --monthly or --yearly, or --renews YYYY-MM-DD|DAY")
	}
	if *monthly && *yearly {
		return e, usageError("a subscription is billed --monthly or --yearly, not both")
	}
	if *price != "" {
		v, err := strconv.ParseFloat(strings.TrimPrefix(*price, "$"), 64)
		if err != nil || v < 0 || v > cost.MaxUSD {
			return e, usageError(fmt.Sprintf("--price takes dollars, from 0 to %d", cost.MaxUSD))
		}
		e.price = &v
	}
	switch {
	case *monthly:
		e.cadence = cost.Monthly
	case *yearly:
		e.cadence = cost.Yearly
	}
	if *renews != "" {
		day, err := renewDay(*renews, now)
		if err != nil {
			return e, err
		}
		e.renews = day
	}
	return e, nil
}

// renewDay is a next charge as a person gives it, a YYYY-MM-DD day or a
// day of the month, as a day: a day of the month is its next one in this
// device's time zone, today's included.
func renewDay(s string, now time.Time) (string, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 || n > 31 {
			return "", usageError("--renews takes a day of the month from 1 to 31, or YYYY-MM-DD")
		}
		return cost.RenewsOn(n, now, time.Local), nil
	}
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return "", usageError("--renews takes YYYY-MM-DD, or a day of the month")
	}
	return s, nil
}

// apply records the change in sub, whose plan is plan, at now.
func (e costChange) apply(sub *cost.Sub, plan string, now time.Time) error {
	a := &sub.Answer
	switch e.cmd {
	case "skip":
		sub.Skip(e.skip)
		return nil
	case "confirm":
		a.Confirm(now)
		return nil
	case "cancel":
		a.Cancel(now)
		return nil
	case "reset":
		a.Reset(now)
		return nil
	}
	if e.price != nil {
		if err := a.SetPrice(e.price, plan, now); err != nil {
			return err
		}
	}
	if e.cadence != "" {
		if err := a.SetCadence(e.cadence, now); err != nil {
			return err
		}
	}
	if e.renews != "" {
		if err := a.SetRenews(e.renews, now); err != nil {
			return err
		}
	}
	return nil
}

// done says what the change did to the subscription t, saved as s.
func (e costChange) done(t account, s *cost.Sub, now time.Time) string {
	who := t.provider + " " + snapshot.Printable(t.label)
	const team = "; the team sees it after the next run"
	switch e.cmd {
	case "confirm":
		return who + " is still paid as of today; nobody is asked again for 35 days" + team
	case "cancel":
		return who + " is cancelled: it counts in no total, and shows as ended for 30 days" + team
	case "reset":
		return who + ": the typed price, cadence, and charge day are cleared, and the list price counts again" + team
	case "skip":
		return who + ": the " + strings.ReplaceAll(e.skip, "_", " ") + " question is not asked again on this device"
	}
	a := s.Answer
	var parts []string
	if a.Price != nil {
		each := "a month"
		if a.Cadence == cost.Yearly {
			each = "a year"
		}
		p := "$" + strconv.FormatFloat(*a.Price, 'f', -1, 64) + " " + each
		if a.PriceFor != "" {
			p += " for " + a.PriceFor
		}
		parts = append(parts, p)
	}
	if a.Cadence != "" && a.Price == nil {
		parts = append(parts, "billed "+a.Cadence)
	}
	if a.Renews != "" {
		if next, ok := cost.NextCharge(a.Renews, cmp.Or(a.Cadence, cost.Monthly), now, time.Local); ok {
			parts = append(parts, "next charge "+next.Format("Mon 2 Jan 2006"))
		}
	}
	return who + ": " + strings.Join(parts, ", ") + team
}

// planOf is the plan of account t as this device and the team's last read
// say: this device's own reading, else the newest of another device's.
func planOf(d state.Dir, st *state.State, self string, t account) string {
	for _, a := range st.Accounts {
		if a != nil && a.Provider == t.provider && snapshot.LabelKey(a.Label) == snapshot.LabelKey(t.label) && a.Plan != "" {
			return a.Plan
		}
	}
	key, err := team.Load(d.KeyFile())
	if err != nil {
		return ""
	}
	cache, err := collect.LoadTeamCache(d)
	if err != nil || cache.Team != key.Fingerprint() {
		return ""
	}
	plan, at := "", time.Time{}
	for _, doc := range cache.Docs {
		if doc.Device == self || !doc.CollectedAt.After(at) {
			continue
		}
		for _, a := range doc.Accounts {
			if l, err := key.Open(a.Label); err == nil && a.Provider == t.provider && snapshot.LabelKey(l) == snapshot.LabelKey(t.label) && a.Plan != "" {
				plan, at = a.Plan, doc.CollectedAt
			}
		}
	}
	return plan
}

// costRunner is what the interactive view runs for `e` and its questions:
// a cost command, its words in a line.
func costRunner(args []string) (string, error) {
	var b strings.Builder
	err := costEdit(args, &b)
	return strings.TrimSpace(b.String()), err
}

// askCost asks a person, on the terminal, the questions of the report the
// first run printed after the guide, one at a time: an empty line takes
// the suggestion, s skips a question for good, and S asks nothing more on
// this device.
func askCost(qs []view.Question, in io.Reader, out io.Writer) {
	if len(qs) == 0 {
		return
	}
	lines := bufio.NewScanner(in)
	ask := func(prompt string) (string, bool) {
		fmt.Fprint(out, "  "+prompt+": ")
		if !lines.Scan() {
			fmt.Fprintln(out)
			return "", false
		}
		return strings.TrimSpace(lines.Text()), true
	}
	fmt.Fprintln(out, "\nCOST  a few questions, so that the report knows what the team pays; s skips one, S asks nothing more")
	for _, q := range qs {
		fmt.Fprintln(out, "\n"+q.Text)
		account := q.Provider + ":" + q.Account
		var args []string
		var answer string
		var ok bool
		switch q.Kind {
		case cost.AskRenewal:
			hint := "next charge, YYYY-MM-DD or a day of the month"
			if q.Suggestion != nil {
				hint += "; Enter takes " + *q.Suggestion
			}
			if answer, ok = ask(hint); !ok {
				return
			}
			if answer == "" && q.Suggestion != nil {
				answer = *q.Suggestion
			}
			if answer != "" && answer != "s" && answer != "S" {
				args = []string{"set", account, "--renews", answer}
				if len(q.Choices) > 1 {
					c, ok := ask("billed m monthly or y yearly; Enter takes monthly")
					if !ok {
						return
					}
					if strings.HasPrefix(strings.ToLower(c), "y") {
						args = append(args, "--yearly")
					}
				}
			}
		case cost.AskPrice:
			if answer, ok = ask("price a month, in USD"); !ok {
				return
			}
			if answer != "" && answer != "s" && answer != "S" {
				args = []string{"set", account, "--price", answer}
			}
		case cost.AskStillPaid:
			if answer, ok = ask("y still paid, n cancelled"); !ok {
				return
			}
			switch strings.ToLower(answer) {
			case "y", "yes":
				args = []string{"confirm", account}
			case "n", "no":
				args = []string{"cancel", account}
			}
		}
		switch answer {
		case "s":
			args = []string{"skip", account, q.Kind}
		case "S":
			args = []string{"never-ask"}
		}
		if len(args) == 0 {
			continue
		}
		said, err := costRunner(args)
		if err != nil {
			said = "ai-usage: " + err.Error()
		}
		fmt.Fprintln(out, "  "+said)
		if answer == "S" {
			return
		}
	}
}

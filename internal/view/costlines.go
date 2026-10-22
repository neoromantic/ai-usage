package view

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// costRow is one row of COST: a subscription's price, what the team used
// of it, its next charge, and its state.
type costRow struct {
	s     *CostSubscription
	here  bool
	name  string
	plan  string
	price chunks
	// mark is after the price: * typed, or none.
	mark        string
	used, pct   string
	next, notes string
	ended       bool
}

// costLayout is the widths of the COST columns, and which of the columns
// that drop as the page narrows are still shown.
type costLayout struct {
	acct, plan, price, used, pct, next, notes int

	showPlan, showPct bool
}

func (l costLayout) width() int {
	n := 2 + l.acct + 2 + l.price + 1 + 2 + l.used + 2 + l.next
	if l.showPlan {
		n += 2 + l.plan
	}
	if l.showPct {
		n += 2 + l.pct
	}
	if l.notes > 0 {
		n += 2 + l.notes
	}
	return n
}

// costSection is COST: what each subscription costs a month, what the team
// used of it over the last 4 weeks, when it charges next, and its state
// when it is not active, one provider at a time, as SUBSCRIPTIONS groups
// them. It is left out when the report has no cost, or no subscription.
func (p *page) costSection() []chunks {
	c := p.r.Cost
	if c == nil || len(c.Subscriptions) == 0 {
		return nil
	}
	type group struct {
		provider string
		rows     []costRow
	}
	var groups []group
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if len(groups) == 0 || groups[len(groups)-1].provider != s.Provider {
			groups = append(groups, group{provider: s.Provider})
		}
		g := &groups[len(groups)-1]
		g.rows = append(g.rows, p.costRow(s))
	}
	l := costLayout{plan: width("PLAN"), price: width("$/MO"), used: width("USED"), next: width("NEXT"), showPlan: true, showPct: true}
	for _, g := range groups {
		l.acct = max(l.acct, width(g.provider))
		for _, r := range g.rows {
			l.acct = max(l.acct, width(r.name))
			l.plan = max(l.plan, min(width(r.plan), planWidth))
			l.price = max(l.price, r.price.width())
			l.used, l.pct = max(l.used, width(r.used)), max(l.pct, width(r.pct))
			l.next, l.notes = max(l.next, width(r.next)), max(l.notes, width(r.notes))
		}
	}
	// The notes say a state: they are cut, not dropped.
	l.notes = min(l.notes, 24)
	for _, drop := range []func(){
		func() { l.showPlan = false },
		func() { l.showPct = false },
	} {
		if l.width() <= p.w {
			break
		}
		drop()
	}
	if over := l.width() - p.w; over > 0 {
		l.acct = max(l.acct-over, minAccount)
	}
	if over := l.width() - p.w; over > 0 && l.notes > 0 {
		l.notes = max(l.notes-over, 8)
	}

	out := []chunks{p.costTitle(c)}
	for i, g := range groups {
		out = append(out, nil)
		head := chunks{p.space(2)}
		head = append(head, p.left(p.bold(strings.ToUpper(g.provider)), l.acct)...)
		if i == 0 {
			if l.showPlan {
				head = append(head, p.space(2))
				head = append(head, p.left(p.muted("PLAN"), l.plan)...)
			}
			head = append(head, p.space(2))
			head = append(head, p.right(p.muted("$/MO"), l.price)...)
			head = append(head, p.space(1))
			head = append(head, p.space(2))
			head = append(head, p.right(p.muted("USED"), l.used)...)
			if l.showPct {
				head = append(head, p.space(2+l.pct))
			}
			head = append(head, p.space(2))
			head = append(head, p.left(p.muted("NEXT"), l.next)...)
		}
		out = append(out, head)
		for _, r := range g.rows {
			p.pick(len(out), Pick{Provider: r.s.Provider, Label: r.s.Account})
			out = append(out, p.costLine(l, r))
		}
	}
	return out
}

// costTitle is COST's title: the team's total a month, how much of it the
// team used over the last 4 weeks, and the next charge, as much as fits.
func (p *page) costTitle(c *Cost) chunks {
	g := p.g
	total := usd(c.TotalUSD) + "/mo"
	if c.WithoutPrice > 0 {
		total += " +" + strconv.Itoa(c.WithoutPrice) + " without a price"
	}
	used, over := "", ""
	if c.UsedUSD != nil && c.UsedPct != nil {
		est := ""
		if c.UsedEstimated {
			est = g.silent
			p.mark("costEstimated")
		}
		used = g.sep + "used " + est + usd(*c.UsedUSD) + " (" + wholePercent(*c.UsedPct) + "%)"
		over = " over the last 4 weeks"
	}
	next := ""
	if n := c.NextCharge; n != nil {
		next = g.sep + "next: " + n.At.In(p.loc).Format("Mon 2 Jan")
		if n.USD != nil {
			next += ", " + usd(*n.USD)
		}
	}
	head := chunks{p.bold("COST"), p.plain("  "), p.plain(total)}
	for _, rest := range []string{used + over + next, used + next, used, ""} {
		if t := append(head, p.muted(rest)); t.width() <= p.w || rest == "" {
			return t
		}
	}
	return head
}

// costRow is a subscription's row of COST.
func (p *page) costRow(s *CostSubscription) costRow {
	g := p.g
	r := costRow{s: s, here: s.Current, name: p.txt(s.Name), plan: p.txt(planOf(s)), ended: s.State == CostEnded, next: g.none}
	if a := p.account(s.Provider, s.Account); a != nil {
		r.name = p.txt(shownLabel(a))
	} else if s.Alias != nil && s.Name == *s.Alias {
		r.name = p.txt(*s.Alias)
	} else {
		r.name = p.txt(shortID(s.Account))
	}
	switch {
	case s.PriceUSD == nil:
		r.price = chunks{p.plain("?")}
		p.mark("unknown")
	default:
		r.price = chunks{p.plain(usd(*s.PriceUSD))}
	}
	if s.PriceSource == PriceTyped {
		r.mark = "*"
		p.mark("typed")
	}
	switch {
	case s.UsedEstimated && s.UsedUSD != nil:
		r.used = g.silent + usd(*s.UsedUSD)
		p.mark("costEstimated")
	case s.UsedUSD != nil:
		r.used = usd(*s.UsedUSD)
	case s.PriceUSD == nil:
		r.used = "?"
		p.mark("unknown")
	default:
		r.used = g.none
		p.mark("none")
	}
	if s.UsedPct != nil {
		r.pct = wholePercent(*s.UsedPct) + "%"
	}
	if s.RenewsAt != nil {
		r.next = s.RenewsAt.In(p.loc).Format("Mon 2 Jan")
	} else {
		p.mark("none")
	}
	var notes []string
	switch s.State {
	case CostIdle:
		w := "idle"
		if s.IdleSince != nil {
			w += " " + age(p.now.Sub(*s.IdleSince))
		}
		notes = append(notes, w)
	case CostUnconfirmed:
		notes = append(notes, "still paid?")
	case CostEnded:
		notes = append(notes, "ended")
	}
	if s.TypedFor != nil {
		notes = append(notes, "typed for "+p.txt(*s.TypedFor))
	}
	if s.BilledThrough != nil {
		notes = append(notes, p.txt(*s.BilledThrough))
	}
	r.notes = strings.Join(notes, g.sep)
	return r
}

func (p *page) costLine(l costLayout, r costRow) chunks {
	g := p.g
	ink := p.plain
	if r.ended {
		// An ended subscription counts in no total.
		ink = p.muted
	}
	out := chunks{p.space(1), p.space(1)}
	if r.here {
		out[0] = p.accent(g.here)
		p.mark("here")
	}
	out = append(out, p.left(ink(truncMid(r.name, l.acct, g.ell)), l.acct)...)
	if l.showPlan {
		out = append(out, p.space(2))
		out = append(out, p.left(p.muted(truncEnd(r.plan, l.plan, g.ell)), l.plan)...)
	}
	out = append(out, p.space(2))
	out = append(out, p.right(ink(r.price.text()), l.price)...)
	out = append(out, p.left(p.muted(r.mark), 1)...)
	out = append(out, p.space(2))
	used := ink(r.used)
	if r.used == g.none {
		used = p.faint(r.used)
	}
	out = append(out, p.right(used, l.used)...)
	if l.showPct {
		out = append(out, p.space(2))
		out = append(out, p.right(p.muted(r.pct), l.pct)...)
	}
	out = append(out, p.space(2))
	next := ink(r.next)
	if r.next == g.none {
		next = p.faint(r.next)
	}
	out = append(out, p.left(next, l.next)...)
	if r.notes != "" {
		out = append(out, p.space(2), p.muted(truncEnd(r.notes, l.notes, g.ell)))
	}
	return out
}

// moneyTotal is the team's total a month as the header ends with it, or
// nothing when no subscription has a price.
func (p *page) moneyTotal() chunks {
	c := p.r.Cost
	if c == nil || c.TotalUSD <= 0 {
		return nil
	}
	return chunks{p.space(2), p.plain(usd(c.TotalUSD) + "/mo")}
}

// costFacts are what a subscription's detail says of its cost: its price
// and where it comes from, what the team used of it, its next charge, its
// state, who bills it, its price a million weighted tokens, and its plans.
// Each is one run of text, which wraps where it does not fit.
func (p *page) costFacts(s *CostSubscription) []fact {
	g := p.g
	var facts []fact
	add := func(key, val string) { facts = append(facts, fact{key, chunks{p.plain(val)}}) }
	cmd := "ai-usage cost set " + s.Provider + ":" + p.txt(shortID(s.Account))
	switch {
	case s.PriceUSD == nil:
		p.mark("unknown")
		add("PRICE", "?"+g.sep+"the price list has no price for "+p.txt(planOf(s))+"; "+cmd+" --price N")
	default:
		v := usd(*s.PriceUSD) + "/mo"
		if s.Cadence == "yearly" {
			v += " billed yearly, " + usd(*s.PriceUSD*12) + " a year"
		}
		why := "the list price of " + p.txt(planOf(s))
		switch s.PriceSource {
		case PriceTyped:
			why = "typed"
		case PriceStale:
			why += "; the price typed for " + p.txt(*s.TypedFor) + " is not used"
		}
		add("PRICE", v+g.sep+why)
	}
	switch {
	case s.UsedUSD != nil && s.UsedEstimated:
		p.mark("costEstimated")
		add("USED", g.silent+usd(*s.UsedUSD)+g.sep+"all of it, split by tokens: no week of the last 4 is known")
	case s.UsedUSD != nil:
		v := usd(*s.UsedUSD)
		if s.UsedPct != nil {
			v += " (" + wholePercent(*s.UsedPct) + "%)"
		}
		v += " over the last 4 weeks"
		if s.UnusedUSD != nil {
			v += g.sep + "unused " + usd(*s.UnusedUSD)
		}
		add("USED", v)
	case s.UsedPct != nil:
		add("USED", wholePercent(*s.UsedPct)+"% over the last 4 weeks")
	}
	switch {
	case s.RenewsAt != nil:
		add("NEXT", s.RenewsAt.In(p.loc).Format("Mon 2 Jan")+g.sep+s.Cadence)
	case s.State != CostEnded:
		hint := cmd + " --renews DAY"
		if s.SuggestedRenewalDay != nil {
			hint = "its tool says day " + strconv.Itoa(*s.SuggestedRenewalDay) + "; " + hint
		}
		add("NEXT", "not confirmed"+g.sep+hint)
	}
	switch s.State {
	case CostIdle:
		if s.IdleSince != nil {
			add("STATE", "idle since "+s.IdleSince.In(p.loc).Format("2 Jan"))
		}
	case CostUnconfirmed:
		w := "still paid?"
		if at := confirmedAt(*s); at != nil {
			w += " last confirmed " + at.In(p.loc).Format("2 Jan")
		}
		add("STATE", w+g.sep+"ai-usage cost confirm or cancel")
	case CostEnded:
		w := "ended"
		if s.EndedAt != nil {
			w += " " + s.EndedAt.In(p.loc).Format("2 Jan")
		}
		add("STATE", w+", in no total")
	}
	if s.BilledThrough != nil {
		add("BILLED", "through the "+p.txt(*s.BilledThrough)+"; the price may differ")
	}
	if s.USDPerMWeighted != nil {
		add("PER M", cents(*s.USDPerMWeighted)+" a month per M weighted tokens of the last 4 weeks")
	}
	if len(s.Plans) > 0 {
		var parts []string
		for _, pl := range s.Plans {
			parts = append(parts, p.txt(p.planName(s.Provider, pl.Plan))+" since "+pl.At.In(p.loc).Format("2 Jan"))
		}
		add("PLANS", strings.Join(parts, " "+p.arrow()+" "))
	}
	return facts
}

// costConsumers is a subscription's detail table of what each device used
// of it over the last 4 weeks, the most first.
func (p *page) costConsumers(s *CostSubscription) []chunks {
	if len(s.Consumers) == 0 {
		return nil
	}
	name := &dcol{head: "DEVICE"}
	used := &dcol{head: "USED", right: true}
	share := &dcol{head: "SHARE", right: true}
	var marks []chunk
	for _, c := range s.Consumers {
		marks = append(marks, p.rowMark(Row{Device: c.Device, DeviceID: c.DeviceID}))
		name.cells = append(name.cells, chunks{p.plain(p.txt(c.Device))})
		used.cells = append(used.cells, chunks{p.cell(p.dollars(c.USD, s.UsedEstimated))})
		share.cells = append(share.cells, chunks{p.cell(p.percent(&c.Share) + "%")})
	}
	title := chunks{p.bold("COST"), p.plain("  "), p.muted(strconv.Itoa(len(s.Consumers)) + p.g.sep + "by device" + p.g.sep + "last 4 weeks")}
	return append([]chunks{title}, p.table(marks, []*dcol{name, used, share})...)
}

// deviceCost is a device's detail table of what it used of each
// subscription over the last 4 weeks, the most first, and its total.
func (p *page) deviceCost(d *TeamDevice) []chunks {
	c := p.r.Cost
	if c == nil {
		return nil
	}
	type use struct {
		s *CostSubscription
		c CostConsumer
	}
	var uses []use
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if s.State == CostEnded {
			continue
		}
		for _, cs := range s.Consumers {
			if cs.DeviceID == d.Device {
				uses = append(uses, use{s, cs})
			}
		}
	}
	if len(uses) == 0 {
		return nil
	}
	val := func(u use) float64 {
		if u.c.USD == nil {
			return -1
		}
		return *u.c.USD
	}
	slices.SortStableFunc(uses, func(a, b use) int { return cmp.Compare(val(b), val(a)) })
	name := &dcol{head: "ACCOUNT"}
	used := &dcol{head: "USED", right: true}
	share := &dcol{head: "SHARE", right: true}
	var marks []chunk
	for _, u := range uses {
		marks = append(marks, p.space(1))
		label := shortID(u.s.Account)
		if a := p.account(u.s.Provider, u.s.Account); a != nil {
			label = shownLabel(a)
		}
		name.cells = append(name.cells, chunks{p.muted(p.txt(u.s.Provider) + " "), p.plain(p.txt(label))})
		used.cells = append(used.cells, chunks{p.cell(p.dollars(u.c.USD, u.s.UsedEstimated))})
		share.cells = append(share.cells, chunks{p.cell(p.percent(&u.c.Share) + "%")})
	}
	var total *float64
	for _, dv := range c.Devices {
		if dv.DeviceID == d.Device {
			total = dv.USD
		}
	}
	marks = append(marks, p.space(1))
	name.cells = append(name.cells, chunks{p.muted("TOTAL")})
	used.cells = append(used.cells, chunks{p.cell(p.dollars(total, false))})
	share.cells = append(share.cells, chunks{p.cell(p.g.none)})
	title := chunks{p.bold("COST"), p.plain("  "), p.muted(strconv.Itoa(len(uses)) + p.g.sep + "used over the last 4 weeks" + p.g.sep + "share of each subscription")}
	return append([]chunks{title}, p.table(marks, []*dcol{name, used, share})...)
}

// dollars prints dollars that may be unknown, ~ before an estimate.
func (p *page) dollars(v *float64, estimated bool) string {
	switch {
	case v == nil:
		return "?"
	case *v <= 0:
		return p.g.none
	case estimated:
		p.mark("costEstimated")
		return p.g.silent + usd(*v)
	}
	return usd(*v)
}

// costStub is a team account for a subscription only the cost knows, as no
// device has it any more, so that its detail opens.
func costStub(s *CostSubscription) *TeamAccount {
	return &TeamAccount{Label: s.Account, Name: s.Name, Alias: s.Alias, Subscription: true, Plan: s.Plan,
		Devices: []string{}, State: StateUnknown, PerDevice: []DeviceUsage{}, Utilization: []Utilization{}}
}

// moneyMatrix is the matrix's $ mode: the dollars of each subscription
// column, by its provider and label key.
type moneyMatrix map[string]*CostSubscription

// moneyMatrix finds the cost of each subscription column of cols.
func (p *page) moneyMatrix(cols []gridCol) moneyMatrix {
	mx := moneyMatrix{}
	for _, c := range cols {
		if c.c.NoQuota {
			continue
		}
		if s := p.r.Cost.Subscription(c.c.Provider, c.c.Label); s != nil && s.State != CostEnded {
			mx[c.c.Provider+"\x00"+c.c.Label] = s
		}
	}
	return mx
}

func (mx moneyMatrix) of(c gridCol) *CostSubscription { return mx[c.c.Provider+"\x00"+c.c.Label] }

// cell is what the device with the ID used of column c, in dollars.
func (mx moneyMatrix) cell(id string, c gridCol) float64 {
	if s := mx.of(c); s != nil {
		for _, u := range s.Consumers {
			if u.DeviceID == id && u.USD != nil {
				return *u.USD
			}
		}
	}
	return 0
}

// text prints a cell, ~ before an estimate, ? where the price is unknown
// and the device spent tokens on it.
func (mx moneyMatrix) text(p *page, id string, c gridCol) string {
	s := mx.of(c)
	if s == nil {
		return p.g.none
	}
	for _, u := range s.Consumers {
		if u.DeviceID == id {
			return p.dollars(u.USD, s.UsedEstimated)
		}
	}
	return p.g.none
}

// row is what the device with the ID used of every subscription.
func (mx moneyMatrix) row(r Row) float64 {
	var v float64
	for _, s := range mx {
		for _, u := range s.Consumers {
			if u.DeviceID == r.DeviceID && u.USD != nil {
				v += *u.USD
			}
		}
	}
	return v
}

// rowText prints a device's TOTAL in dollars: what Cost.Devices says it
// used, ~ when an estimate is among it.
func (mx moneyMatrix) rowText(p *page, id string) string {
	est := false
	for _, s := range p.r.Cost.Subscriptions {
		for _, u := range s.Consumers {
			est = est || u.DeviceID == id && s.UsedEstimated && s.State != CostEnded
		}
	}
	for _, d := range p.r.Cost.Devices {
		if d.DeviceID == id {
			return p.dollars(d.USD, est)
		}
	}
	return p.g.none
}

// unusedOf is what nobody used of column c, and unused prints it: ? when
// its Used or price is unknown.
func (mx moneyMatrix) unusedOf(c gridCol) float64 {
	if s := mx.of(c); s != nil && s.UnusedUSD != nil {
		return *s.UnusedUSD
	}
	return 0
}

func (mx moneyMatrix) unused(p *page, c gridCol) string {
	s := mx.of(c)
	switch {
	case s == nil:
		return p.g.none
	case s.UnusedUSD == nil && s.PriceUSD == nil:
		return "?"
	case s.UnusedUSD == nil:
		return p.g.none
	}
	return p.dollars(s.UnusedUSD, s.UsedEstimated)
}

// unusedTotal prints what nobody used of every subscription.
func (mx moneyMatrix) unusedTotal(p *page) string {
	return p.dollars(p.r.Cost.UnusedUSD, p.r.Cost.UsedEstimated)
}

// priceOf is column c's price a month, and price prints it: ? when it is
// unknown.
func (mx moneyMatrix) priceOf(c gridCol) float64 {
	if s := mx.of(c); s != nil && s.PriceUSD != nil {
		return *s.PriceUSD
	}
	return 0
}

func (mx moneyMatrix) price(p *page, c gridCol) string {
	s := mx.of(c)
	switch {
	case s == nil:
		return p.g.none
	case s.PriceUSD == nil:
		p.mark("unknown")
		return "?"
	}
	return p.dollars(s.PriceUSD, false)
}

// CostText is what `ai-usage cost` prints: COST, then what each device
// used over the last 4 weeks, what nobody used, and the total, and the
// legend. A report with no cost says so.
func CostText(r Report, o Options) string {
	p := newPage(&r, o)
	body := []chunks{nil}
	if s := p.costSection(); len(s) > 0 {
		body = append(body, s...)
		body = append(body, nil)
		body = append(body, p.costDevices()...)
	} else {
		body = append(body, chunks{p.bold("COST")}, chunks{p.space(2), p.muted("no subscription has been seen on this team's devices yet")})
	}
	out := p.finish(body)
	var b strings.Builder
	b.WriteString(out.Header + "\n")
	for _, l := range out.Body {
		b.WriteString(l + "\n")
	}
	if l := p.legend(); l != "" {
		b.WriteString("\n" + l + "\n")
	}
	return b.String()
}

// costDevices is what each device used of the subscriptions that have not
// ended over the last 4 weeks, the most first, then what nobody used, and
// the team's total a month.
func (p *page) costDevices() []chunks {
	c := p.r.Cost
	name := &dcol{head: "DEVICE"}
	used := &dcol{head: "USED", right: true}
	var marks []chunk
	est := map[string]bool{}
	for _, s := range c.Subscriptions {
		for _, u := range s.Consumers {
			est[u.DeviceID] = est[u.DeviceID] || s.UsedEstimated && s.State != CostEnded
		}
	}
	for _, d := range c.Devices {
		marks = append(marks, p.rowMark(Row{Device: d.Device, DeviceID: d.DeviceID}))
		name.cells = append(name.cells, chunks{p.plain(p.txt(d.Device))})
		used.cells = append(used.cells, chunks{p.cell(p.dollars(d.USD, est[d.DeviceID]))})
	}
	marks = append(marks, p.space(1), p.space(1))
	name.cells = append(name.cells, chunks{p.muted("unused")}, chunks{p.muted("TOTAL")})
	used.cells = append(used.cells, chunks{p.cell(p.dollars(c.UnusedUSD, c.UsedEstimated))}, chunks{p.cell(usd(c.TotalUSD))})
	title := chunks{p.bold("BY DEVICE"), p.plain("  "), p.muted(strconv.Itoa(len(c.Devices)) + p.g.sep + "used over the last 4 weeks" + p.g.sep + "TOTAL a month")}
	return append([]chunks{title}, p.table(marks, []*dcol{name, used})...)
}

package collect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// hermesSess is a Hermes session as logs reads it: its tokens by the billing
// provider of each part, and the provider its main loop bills now.
func hermesSess(id, main string, at time.Time, parts map[string]snapshot.Tokens) logs.Session {
	s := logs.Session{ID: id, Project: "/work/agent", Account: main, Updated: at, Parts: parts}
	for _, t := range parts {
		s.Tokens = s.Tokens.Add(t)
	}
	return s
}

// Hermes on the Codex subscription is linked to the account logged in to
// ~/.codex, and on SuperGrok to the one in ~/.grok. It shows that account's
// quota reading as it is, while its tokens stay Hermes'. The linked account
// shows what Hermes spent on it. Anthropic and API-key routes are not linked.
func TestHermesLinksSubscriptionsToDefaultHomes(t *testing.T) {
	w, o := newWorld(t)
	codex := w.home(t, "codex")
	other := w.extraHome(t, "codex", "codex-work")
	grok := w.home(t, "grok")
	hermes := w.home(t, "hermes")
	codexQuota := quota(t0.Add(-time.Minute), 30, 70)
	w.login("codex", codex, "sam", codexQuota)
	w.login("codex", other, "bea", quota(t0, 5))
	w.login("grok", grok, "sam-x", nil) // no quota reading
	w.sessions("hermes", hermes,
		hermesSess("h1", "openai-codex", t0.Add(-time.Hour), map[string]snapshot.Tokens{
			"openai-codex": tok(1000), "xai-oauth": tok(200), "openrouter": tok(10),
		}),
		hermesSess("h2", "anthropic", t0.Add(-2*time.Hour), map[string]snapshot.Tokens{"anthropic": tok(50)}),
	)
	res := run(t, o)

	h := totalsFor(t, res.State, "hermes", "openai-codex")
	if h.Tokens != tok(1000) || !reflect.DeepEqual(h.Link, &state.Link{Provider: "codex", Label: "sam"}) {
		t.Fatalf("hermes openai-codex = %+v", h)
	}
	// The very same reading: same windows, same time.
	sam := totalsFor(t, res.State, "codex", "sam")
	if h.QuotaFrom != "codex" || h.Quota == nil || !reflect.DeepEqual(h.Quota, sam.Quota) || !h.Quota.At.Equal(codexQuota.At) {
		t.Fatalf("hermes quota = %s %+v, codex %+v", h.QuotaFrom, h.Quota, sam.Quota)
	}
	if sam.Tokens != (snapshot.Tokens{}) || sam.Linked != tok(1000) || sam.LinkedSessions != 1 {
		t.Fatalf("codex sam = %+v", sam)
	}
	if bea := totalsFor(t, res.State, "codex", "bea"); !bea.Linked.Zero() {
		t.Fatalf("a non-default home's account got Hermes usage: %+v", bea)
	}
	// Linked, but the SuperGrok account has no reading: unknown stays unknown.
	x := totalsFor(t, res.State, "hermes", "xai-oauth")
	if x.Tokens != tok(200) || x.Link == nil || x.Link.Label != "sam-x" || x.Quota != nil || x.QuotaFrom != "" {
		t.Fatalf("hermes xai-oauth = %+v", x)
	}
	if g := totalsFor(t, res.State, "grok", "sam-x"); g.Linked != tok(200) {
		t.Fatalf("grok = %+v", g)
	}
	for _, label := range []string{"anthropic", "openrouter"} {
		if a := totalsFor(t, res.State, "hermes", label); a.Link != nil || a.Quota != nil {
			t.Fatalf("hermes %s linked: %+v", label, a)
		}
	}

	// The snapshot carries the reading with where it came from.
	var found bool
	for _, a := range res.Doc.Accounts {
		if a.Provider == "hermes" && a.QuotaFrom != "" {
			found = true
			if a.QuotaFrom != "codex" || a.QuotaAt == nil || !a.QuotaAt.Equal(codexQuota.At) || len(a.Windows) != 2 || a.Windows[0].Percent != 30 {
				t.Fatalf("snapshot hermes account = %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("snapshot has no linked hermes account")
	}
	if err := res.Doc.Validate(time.Time{}); err != nil {
		t.Fatal(err)
	}

	// ~/.codex switches account: new Hermes usage is linked to the new one,
	// and the earlier usage stays with sam.
	w.now = t0.Add(15 * time.Minute)
	w.login("codex", codex, "kim", nil)
	w.sessions("hermes", hermes,
		hermesSess("h1", "openai-codex", w.now, map[string]snapshot.Tokens{
			"openai-codex": tok(1300), "xai-oauth": tok(200), "openrouter": tok(10),
		}),
	)
	res = run(t, o)
	if sam := totalsFor(t, res.State, "codex", "sam"); sam.Linked != tok(1000) {
		t.Fatalf("sam after switch = %+v", sam)
	}
	if kim := totalsFor(t, res.State, "codex", "kim"); kim.Linked != tok(300) {
		t.Fatalf("kim = %+v", kim)
	}
	// kim has no reading yet, so Hermes has none either.
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link.Label != "kim" || h.Quota != nil {
		t.Fatalf("hermes after switch = %+v", h)
	}

	// Nobody logged in to ~/.codex: no link, and new usage is linked to no one.
	w.now = t0.Add(30 * time.Minute)
	w.readings[state.Key("codex", codex)] = probe.Reading{}
	w.askErr[state.Key("codex", codex)] = notLoggedIn("codex")
	w.sessions("hermes", hermes,
		hermesSess("h1", "openai-codex", w.now, map[string]snapshot.Tokens{"openai-codex": tok(1400)}),
	)
	res = run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link != nil || h.Quota != nil || h.Tokens != tok(1400) {
		t.Fatalf("hermes after logout = %+v", h)
	}
	if kim := totalsFor(t, res.State, "codex", "kim"); kim.Linked != tok(300) {
		t.Fatalf("kim after logout = %+v", kim)
	}
}

// Each billing provider's part grows on its own: a session that moves from
// one route to another does not hand the first route's tokens over.
func TestHermesPartsGrowOnTheirOwn(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "hermes")
	w.sessions("hermes", h, hermesSess("h1", "openai-codex", t0, map[string]snapshot.Tokens{"openai-codex": tok(100)}))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.sessions("hermes", h, hermesSess("h1", "xai-oauth", w.now, map[string]snapshot.Tokens{
		"openai-codex": tok(100), "xai-oauth": tok(40), "": tok(5),
	}))
	res := run(t, o)
	e := res.State.Sessions[state.Key("hermes", "h1")]
	// A part that names no provider is the session's main route's.
	want := map[string]snapshot.Tokens{"openai-codex": tok(100), "xai-oauth": tok(45)}
	if !reflect.DeepEqual(e.By, want) || !reflect.DeepEqual(e.Parts, want) || e.Seen != tok(145) {
		t.Fatalf("ledger = %+v", e)
	}
	if g := growthOf(lastSample(t, o), "hermes", "xai-oauth"); g != tok(45) {
		t.Fatalf("sample growth = %+v", g)
	}
	if !IsCurrent(res.State, "hermes", "xai-oauth") {
		t.Fatalf("current = %v", res.State.Current)
	}
}

// A ledger from before parts were kept counted each Hermes session under the
// accounts in By. Its first read with parts adds only what grew.
func TestHermesPartsContinueOldLedger(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "hermes")
	w.sessions("hermes", h, logs.Session{ID: "h1", Account: "openai-codex", Tokens: tok(100), Updated: t0})
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	w.sessions("hermes", h, hermesSess("h1", "openai-codex", w.now, map[string]snapshot.Tokens{
		"openai-codex": tok(120), "openrouter": tok(7),
	}))
	res := run(t, o)
	if by := res.State.Sessions[state.Key("hermes", "h1")].By; !reflect.DeepEqual(by, map[string]snapshot.Tokens{"openai-codex": tok(120), "openrouter": tok(7)}) {
		t.Fatalf("by = %+v", by)
	}
}

// A session whose own row names no billing provider, as a row Hermes makes
// for auxiliary calls alone, bills only through its parts. Being the newest,
// it does not make an account with no tokens the one Hermes bills now.
func TestHermesSessionWithoutRouteKeepsCurrent(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "hermes")
	w.sessions("hermes", h,
		hermesSess("h1", "openai-codex", t0.Add(-time.Hour), map[string]snapshot.Tokens{"openai-codex": tok(100)}),
		hermesSess("aux", "", t0, map[string]snapshot.Tokens{"openai-codex": tok(40)}),
	)
	res := run(t, o)
	if !IsCurrent(res.State, "hermes", "openai-codex") || hasTotals(res.State, "hermes", UnknownAccount) {
		t.Fatalf("current = %v, totals = %+v", res.State.Current, Totals(res.State))
	}
	if a := totalsFor(t, res.State, "hermes", "openai-codex"); a.Tokens != tok(140) || a.Sessions != 2 {
		t.Fatalf("openai-codex = %+v", a)
	}
}

// A ledger from before parts were kept counted a session under the route
// its row billed, sub-agents on other routes included. The first read with
// parts splits the same tokens by route, and counts only what the session
// grew in all. From then on each part grows on its own.
func TestHermesPartsDoNotRecountOldLedger(t *testing.T) {
	w, o := newWorld(t)
	h := w.home(t, "hermes")
	// The old reader: a Codex-route session with a SuperGrok sub-agent.
	w.sessions("hermes", h, logs.Session{ID: "h1", Account: "openai-codex", Tokens: tok(150), Updated: t0})
	run(t, o)

	sum := func(res *Result) snapshot.Tokens {
		var total snapshot.Tokens
		for _, t := range res.State.Sessions[state.Key("hermes", "h1")].By {
			total = total.Add(t)
		}
		return total
	}
	// The same tokens, by route, plus a title call the old reader never saw.
	w.now = t0.Add(15 * time.Minute)
	w.sessions("hermes", h, hermesSess("h1", "openai-codex", t0, map[string]snapshot.Tokens{
		"openai-codex": tok(100), "xai-oauth": tok(50), "openrouter": tok(6),
	}))
	res := run(t, o)
	if got := sum(res); got != tok(156) {
		t.Fatalf("after the first read with parts: %+v, want %+v", res.State.Sessions[state.Key("hermes", "h1")].By, tok(156))
	}

	w.now = t0.Add(30 * time.Minute)
	w.sessions("hermes", h, hermesSess("h1", "openai-codex", w.now, map[string]snapshot.Tokens{
		"openai-codex": tok(100), "xai-oauth": tok(80), "openrouter": tok(6),
	}))
	res = run(t, o)
	if got := sum(res); got != tok(186) {
		t.Fatalf("after growth: %+v", res.State.Sessions[state.Key("hermes", "h1")].By)
	}
	if g := growthOf(lastSample(t, o), "hermes", "xai-oauth"); g != tok(30) {
		t.Fatalf("xai-oauth growth = %+v", g)
	}
}

// A Hermes home the person named a Codex home for bills through the login
// there, and so do the profiles inside it, whatever path names it. Other
// Hermes homes keep the default home's login. The Hermes account shows the
// quota of the login most of its tokens went through, and with no session
// read, keeps its link.
func TestHermesQuotaFromNamedHome(t *testing.T) {
	w, o := newWorld(t)
	codex := w.home(t, "codex")
	hermes := w.home(t, "hermes")
	root := filepath.Dir(w.userHome)
	bots := filepath.Join(root, "bots", ".codex")
	botsGrok := filepath.Join(root, "bots", ".grok")
	agent := filepath.Join(root, "bots", ".hermes-agent")
	profile := filepath.Join(agent, "profiles", "helper")
	for _, d := range []string{bots, botsGrok, profile} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The entry names the home by a symlink; the run finds it by its path.
	alias := filepath.Join(root, "agent-alias")
	if err := os.Symlink(agent, alias); err != nil {
		t.Fatal(err)
	}
	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Homes = map[string][]string{"codex": {bots}, "grok": {botsGrok}, "hermes": {agent}}
	cfg.QuotaFrom = map[string]map[string]string{alias: {"codex": bots, "grok": botsGrok}}
	if err := o.Dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	botsQuota := quota(t0.Add(-time.Minute), 40, 60)
	w.login("codex", codex, "sam", quota(t0, 5, 10))
	w.login("codex", bots, "bots", botsQuota)
	w.login("grok", botsGrok, "bots-x", nil)
	w.sessions("hermes", agent, hermesSess("a1", "openai-codex", t0.Add(-time.Hour), map[string]snapshot.Tokens{
		"openai-codex": tok(1000), "xai-oauth": tok(30),
	}))
	// A part that names no route is the session's own route's.
	w.sessions("hermes", profile, hermesSess("p1", "openai-codex", t0.Add(-30*time.Minute), map[string]snapshot.Tokens{"": tok(500)}))
	w.sessions("hermes", hermes, hermesSess("d1", "openai-codex", t0.Add(-2*time.Hour), map[string]snapshot.Tokens{"openai-codex": tok(70)}))
	res := run(t, o)

	h := totalsFor(t, res.State, "hermes", "openai-codex")
	if !reflect.DeepEqual(h.Link, &state.Link{Provider: "codex", Label: "bots"}) || h.QuotaFrom != "codex" || h.Quota == nil || !h.Quota.At.Equal(botsQuota.At) {
		t.Fatalf("hermes openai-codex = %+v", h)
	}
	if b := totalsFor(t, res.State, "codex", "bots"); b.Linked != tok(1500) || b.LinkedSessions != 2 || !b.Tokens.Zero() {
		t.Fatalf("codex bots = %+v", b)
	}
	if s := totalsFor(t, res.State, "codex", "sam"); s.Linked != tok(70) {
		t.Fatalf("codex sam = %+v", s)
	}
	if x := totalsFor(t, res.State, "hermes", "xai-oauth"); !reflect.DeepEqual(x.Link, &state.Link{Provider: "grok", Label: "bots-x"}) {
		t.Fatalf("hermes xai-oauth = %+v", x.Link)
	}

	// The default home's session is now the newest, but most tokens still
	// went through the bots' login, so the link holds.
	w.now = t0.Add(15 * time.Minute)
	w.sessions("hermes", hermes, hermesSess("d1", "openai-codex", w.now, map[string]snapshot.Tokens{"openai-codex": tok(90)}))
	res = run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "bots" {
		t.Fatalf("hermes after a default-home session = %+v", h.Link)
	}
	// Once most tokens go through the person's own login, it follows.
	w.now = t0.Add(30 * time.Minute)
	w.sessions("hermes", hermes, hermesSess("d1", "openai-codex", w.now, map[string]snapshot.Tokens{"openai-codex": tok(5000)}))
	res = run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "sam" {
		t.Fatalf("hermes after the default home took most = %+v", h.Link)
	}

	// No session read at all: the link stays.
	w.now = t0.Add(45 * time.Minute)
	for _, home := range []string{agent, profile, hermes} {
		w.sessions("hermes", home)
	}
	res = run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "sam" {
		t.Fatalf("hermes with no sessions = %+v", h.Link)
	}
}

// A profile that is a link to a folder elsewhere is still its home's
// profile. A run that could not read every Hermes home keeps the link: the
// homes it read are not the majority.
func TestHermesQuotaFromLinkedProfileAndPartialRead(t *testing.T) {
	w, o := newWorld(t)
	codex := w.home(t, "codex")
	hermes := w.home(t, "hermes")
	root := filepath.Dir(w.userHome)
	bots := filepath.Join(root, "bots", ".codex")
	agent := filepath.Join(root, "bots", ".hermes-agent")
	elsewhere := filepath.Join(root, "profiles-elsewhere", "helper")
	for _, d := range []string{bots, filepath.Join(agent, "profiles"), elsewhere} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(agent, "profiles", "helper")
	if err := os.Symlink(elsewhere, profile); err != nil {
		t.Fatal(err)
	}
	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Homes = map[string][]string{"codex": {bots}, "hermes": {agent}}
	cfg.QuotaFrom = map[string]map[string]string{agent: {"codex": bots}}
	if err := o.Dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	w.login("codex", codex, "sam", quota(t0, 5))
	w.login("codex", bots, "bots", quota(t0, 40))
	w.sessions("hermes", profile, hermesSess("p1", "openai-codex", t0.Add(-time.Hour), map[string]snapshot.Tokens{"openai-codex": tok(1000)}))
	w.sessions("hermes", hermes, hermesSess("d1", "openai-codex", t0.Add(-2*time.Hour), map[string]snapshot.Tokens{"openai-codex": tok(70)}))
	res := run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "bots" {
		t.Fatalf("hermes with a linked profile = %+v", h.Link)
	}
	if b := totalsFor(t, res.State, "codex", "bots"); b.Linked != tok(1000) {
		t.Fatalf("codex bots = %+v", b)
	}

	// The profile's read fails; the default home alone would say sam.
	w.now = t0.Add(15 * time.Minute)
	w.readErr[state.Key("hermes", profile)] = os.ErrPermission
	w.sessions("hermes", profile)
	w.sessions("hermes", hermes, hermesSess("d1", "openai-codex", w.now, map[string]snapshot.Tokens{"openai-codex": tok(90)}))
	res = run(t, o)
	if h := totalsFor(t, res.State, "hermes", "openai-codex"); h.Link == nil || h.Link.Label != "bots" {
		t.Fatalf("hermes after a partial read = %+v", h.Link)
	}
}

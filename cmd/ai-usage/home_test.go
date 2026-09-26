package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/state"
)

// home add registers homes by absolute path and, for Hermes, the home whose
// login they bill through, which it reads too. home remove undoes it.
func TestHomeCommands(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	root := filepath.Dir(d.home)
	bots := filepath.Join(root, "bots")
	for _, dir := range []string{".codex", ".grok", ".hermes-a/profiles/p", ".hermes-b", ".codex-gone"} {
		if err := os.MkdirAll(filepath.Join(bots, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bots, ".hermes-a/profiles/p/state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(bots)
	out := d.ok("home", "add", "hermes", ".hermes-a", ".hermes-b", "--quota-from", "codex:.codex")
	codex, a, b := filepath.Join(bots, ".codex"), filepath.Join(bots, ".hermes-a"), filepath.Join(bots, ".hermes-b")
	lines := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		lines[strings.Join(strings.Fields(line), " ")] = true
	}
	for _, want := range []string{
		"codex " + codex + " added",
		"hermes " + a + " added · quota from codex " + codex,
		filepath.Join(a, "profiles", "p") + " quota from codex " + codex,
		b + " added · quota from codex " + codex,
	} {
		if !lines[want] {
			t.Fatalf("home add output lacks %q:\n%s", want, out)
		}
	}
	cfg := d.config()
	if !reflect.DeepEqual(cfg.Homes, map[string][]string{"codex": {codex}, "hermes": {a, b}}) ||
		!reflect.DeepEqual(cfg.QuotaFrom, map[string]map[string]string{a: {"codex": codex}, b: {"codex": codex}}) {
		t.Fatalf("config = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}

	// A home can take each harness's quota from its own home.
	grok := filepath.Join(bots, ".grok")
	d.ok("home", "add", "hermes", ".hermes-a", "--quota-from=grok:.grok")
	if got := d.config().QuotaFrom[a]; !reflect.DeepEqual(got, map[string]string{"codex": codex, "grok": grok}) {
		t.Fatalf("quota from = %+v", got)
	}

	d.ok("home", "remove", "hermes", b)
	cfg = d.config()
	if !reflect.DeepEqual(cfg.Homes["hermes"], []string{a}) || len(cfg.QuotaFrom) != 1 {
		t.Fatalf("after remove = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}

	// An added home that is gone is listed as missing, not dropped.
	gone := filepath.Join(bots, ".codex-gone")
	d.ok("home", "add", "codex", gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if out := d.ok("home"); !strings.Contains(strings.Join(strings.Fields(out), " "), "codex "+gone+" missing") {
		t.Fatalf("home lacks the missing home:\n%s", out)
	}
	d.ok("home", "remove", "codex", gone)

	// The default Hermes home can be named a quota home, and unnamed again,
	// with no other Hermes home added.
	defHermes := filepath.Join(d.home, ".hermes")
	if err := os.MkdirAll(defHermes, 0o700); err != nil {
		t.Fatal(err)
	}
	d.ok("home", "add", "hermes", defHermes, "--quota-from", "codex:"+codex, "--quota-from", "grok:"+grok)
	if got := d.config().QuotaFrom[defHermes]; !reflect.DeepEqual(got, map[string]string{"codex": codex, "grok": grok}) {
		t.Fatalf("quota from = %+v", d.config().QuotaFrom)
	}
	d.ok("home", "remove", "hermes", defHermes)
	cfg = d.config()
	if _, ok := cfg.QuotaFrom[defHermes]; ok {
		t.Fatalf("quota from after remove = %+v", cfg.QuotaFrom)
	}

	for _, bad := range [][]string{
		{"home", "add", "hermes"},
		{"home", "add", "nope", ".codex"},
		{"home", "add", "codex", ".codex", "--quota-from", "codex:.codex"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "claude:.codex"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from"},
		{"home", "remove", "hermes", ".hermes-b", "--quota-from", "codex:.codex"},
		{"home", "frob"},
		// An empty directory, as from an unset variable, is not the
		// working directory.
		{"home", "add", "hermes", ""},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "codex:"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from="},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "codex:.codex", "--quota-from", "codex:.grok"},
	} {
		if r := d.run("", bad...); r.code != 2 {
			t.Fatalf("%v: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	for _, bad := range [][]string{
		{"home", "add", "hermes", "missing"},
		{"home", "remove", "hermes", defHermes},
		// A Hermes home is not a Codex home.
		{"home", "remove", "codex", a},
	} {
		if r := d.run("", bad...); r.code != 1 {
			t.Fatalf("%v: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	// Removing the home Hermes takes its quota from would quietly move it to
	// the default login.
	if r := d.run("", "home", "remove", "codex", codex); r.code != 1 || !strings.Contains(r.stderr, "take their quota from") {
		t.Fatalf("removing a quota home: exit %d, %s", r.code, r.stderr)
	}

	// A config with no added homes at all: naming the default Hermes home
	// after the default Codex home adds none, and unnaming it is fine.
	e := newDevice(t)
	for _, dir := range []string{".hermes", ".codex"} {
		if err := os.MkdirAll(filepath.Join(e.home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.ok("home", "add", "hermes", filepath.Join(e.home, ".hermes"), "--quota-from", "codex:"+filepath.Join(e.home, ".codex"))
	e.ok("home", "remove", "hermes", filepath.Join(e.home, ".hermes"))
	if cfg := e.config(); len(cfg.Homes) != 0 || len(cfg.QuotaFrom) != 0 {
		t.Fatalf("config = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}
	if !reflect.DeepEqual(d.config().Homes, cfg.Homes) {
		t.Fatal("a rejected command changed the config")
	}
}

// A home another collector reads now, such as a bot's in its own container,
// is removed with the sessions counted from it, so the team does not count
// them twice.
func TestHomeRemoveForgetsItsSessions(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.codex("c-own", "/work/api")
	bot := filepath.Join(d.home, "bot", ".codex")
	d.write("bot/.codex/sessions/2026/09/23/rollout-c-bot.jsonl",
		`{"timestamp":"2026-09-23T10:00:00Z","type":"session_meta","payload":{"id":"c-bot","cwd":"/work/bot"}}`+"\n"+
			`{"timestamp":"2026-09-23T10:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":900,"output_tokens":90}}}}`+"\n")
	// The bot's home was seeded with a copy of the default one: that session
	// stays counted, since the default home is still read.
	own0, err := os.ReadFile(filepath.Join(d.home, ".codex/sessions/2026/09/23/rollout-c-own.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	d.write("bot/.codex/sessions/2026/09/23/rollout-c-own.jsonl", string(own0))
	d.ok("home", "add", "codex", bot)
	d.ok("collect", "--quiet", "--offline")
	own, botKey := state.Key("codex", "c-own"), state.Key("codex", "c-bot")
	before := d.state().Sessions[own]
	if before == nil || d.state().Sessions[botKey] == nil {
		t.Fatalf("sessions before = %v", d.state().Sessions)
	}

	if out := d.ok("home", "remove", "codex", bot, "--forget"); !strings.Contains(out, "forgot 1 ") {
		t.Fatalf("home remove --forget printed:\n%s", out)
	}
	if st := d.state(); st.Sessions[botKey] != nil || !reflect.DeepEqual(st.Sessions[own], before) {
		t.Fatalf("sessions after = %v", st.Sessions)
	}
	if cfg := d.config(); len(cfg.Homes) != 0 {
		t.Fatalf("homes after = %v", cfg.Homes)
	}
	d.ok("collect", "--quiet", "--offline")
	if d.state().Sessions[botKey] != nil {
		t.Fatal("the next run read the removed home again")
	}
	// Forgetting can be tried again once the home is out of the config.
	if out := d.ok("home", "remove", "codex", bot, "--forget"); !strings.Contains(out, "forgot 0 ") {
		t.Fatalf("second --forget printed:\n%s", out)
	}

	// The default home is always read, so its sessions would only be counted
	// again from nothing.
	if r := d.run("", "home", "remove", "codex", filepath.Join(d.home, ".codex"), "--forget"); r.code != 1 || d.state().Sessions[own] == nil {
		t.Fatalf("forgetting the default home: exit %d, %s", r.code, r.stderr)
	}
	if r := d.run("", "home", "add", "codex", bot, "--forget"); r.code != 2 {
		t.Fatalf("home add --forget: exit %d, %s", r.code, r.stderr)
	}
	// A home runs still find is not forgotten: its sessions would only be
	// counted again from nothing.
	named := filepath.Join(d.home, "named", ".codex")
	d.write("named/.codex/sessions/2026/09/23/rollout-c-named.jsonl",
		strings.ReplaceAll(strings.ReplaceAll(string(own0), "c-own", "c-named"), "/work/api", "/work/named"))
	t.Setenv("CODEX_HOME", named)
	d.ok("collect", "--quiet", "--offline")
	if r := d.run("", "home", "remove", "codex", named, "--forget"); r.code != 1 || !strings.Contains(r.stderr, "still read") ||
		d.state().Sessions[state.Key("codex", "c-named")] == nil {
		t.Fatalf("forgetting a home runs still find: exit %d, %s", r.code, r.stderr)
	}
	t.Setenv("CODEX_HOME", "")
	// A home that cannot be read changes nothing.
	gone := filepath.Join(d.home, "gone")
	if r := d.run("", "home", "remove", "codex", gone, "--forget"); r.code != 1 || !strings.Contains(r.stderr, "cannot read") {
		t.Fatalf("forgetting a missing home: exit %d, %s", r.code, r.stderr)
	}
}

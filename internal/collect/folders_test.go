package collect

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// tree makes folders and files in a new temporary folder, which it returns
// with its links resolved. An entry is a folder, or name=content for a
// file, $ROOT in which is that folder.
func tree(t *testing.T, entries ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name, content, file := strings.Cut(e, "=")
		p := filepath.Join(root, filepath.FromSlash(name))
		dir := p
		if file {
			dir = filepath.Dir(p)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if file {
			if err := os.WriteFile(p, []byte(strings.ReplaceAll(content, "$ROOT", root)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// projectsOf is the project of each of dirs, under root, with f.
func projectsOf(t *testing.T, f Folders, root string, dirs ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		p := f.of(filepath.Join(root, filepath.FromSlash(d))).Project
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = filepath.ToSlash(rel)
		}
		out = append(out, p)
	}
	return out
}

// under is each of dirs in root.
func under(root string, dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		out = append(out, filepath.Join(root, filepath.FromSlash(d)))
	}
	return out
}

// ledgerIn is a ledger with a session in each of dirs.
func ledgerIn(dirs ...string) *state.State {
	st := state.NewState()
	for i, d := range dirs {
		st.Sessions[state.Key("claude", fmt.Sprint(i))] = &state.Session{Provider: "claude", Project: d}
	}
	return st
}

// TestFindProjectsInRepositories: a repository's subfolders, gone or not,
// and its linked worktrees, pruned or not, with an absolute or a relative
// gitdir, are its, its commondir file deciding; a submodule is its own
// project, even at worktrees/x, and its linked worktrees are its, as its
// git config names it; a bare repository's worktrees are its, or the
// folder's that holds it when it is hidden, or the checkout's whose .git
// file names it, as git clone --separate-git-dir makes; a folder outside
// any repository is its own.
func TestFindProjectsInRepositories(t *testing.T) {
	root := tree(t,
		"src/app/.git/HEAD=ref: refs/heads/main",
		"src/app/.git/worktrees/feature/commondir=../..",
		"src/app/.git/worktrees/rel/commondir=../..",
		"src/app/.git/modules/lib/HEAD=ref: refs/heads/main",
		"src/app/pkg/ui",
		"src/app/vendor/lib/.git=gitdir: ../../.git/modules/lib\n",
		"src/app/.git/modules/ui/config=[core]\n\tbare = false\n\tworktree = ../../../vendor/ui\n",
		"src/app/.git/modules/ui/worktrees/fix/commondir=../..",
		"src/app/vendor/ui/.git=gitdir: ../../.git/modules/ui",
		"wt/ui/.git=gitdir: $ROOT/src/app/.git/modules/ui/worktrees/fix",
		"src/app/.git/modules/worktrees/x/HEAD=ref: refs/heads/main",
		"src/app/worktrees/x/.git=gitdir: ../../.git/modules/worktrees/x",
		"src/lib/.git/HEAD=ref: refs/heads/main",
		"gits/odd/commondir=$ROOT/src/lib/.git\n",
		"wt/odd/.git=gitdir: $ROOT/gits/odd",
		"wt/feature/.git=gitdir: $ROOT/src/app/.git/worktrees/feature\n",
		"wt/rel/.git=gitdir: ../../src/app/.git/worktrees/rel",
		"wt/pruned/.git=gitdir: $ROOT/src/app/.git/worktrees/pruned",
		"bare/app.git/worktrees/main/commondir=../..",
		"bare/main/.git=gitdir: $ROOT/bare/app.git/worktrees/main",
		"hidden/.bare/worktrees/main/commondir=../..",
		"hidden/.git=gitdir: ./.bare",
		"hidden/main/src/.keep=",
		"hidden/main/.git=gitdir: $ROOT/hidden/.bare/worktrees/main",
		"store/app.git/worktrees/sep/commondir=../..",
		"sep/app/.git=gitdir: $ROOT/store/app.git",
		"wt/sep/.git=gitdir: $ROOT/store/app.git/worktrees/sep",
		"plain/notes",
	)
	dirs := []string{
		"src/app", "src/app/pkg/ui", "src/app/gone/away", "wt/feature", "wt/rel/deeper", "wt/pruned",
		"src/app/vendor/lib", "src/app/vendor/lib/src", "src/app/worktrees/x", "wt/odd", "wt/ui",
		"bare/main", "hidden", "hidden/main/src", "wt/sep", "sep/app",
		"plain", "plain/notes",
	}
	f := FindProjects(ledgerIn(append(under(root, dirs...), "unknown", "Claude app")...))
	want := []string{
		"src/app", "src/app", "src/app", "src/app", "src/app", "src/app",
		"src/app/vendor/lib", "src/app/vendor/lib", "src/app/worktrees/x", "src/lib", "src/app/vendor/ui",
		"bare/app.git", "hidden", "hidden", "sep/app", "sep/app",
		"plain", "plain/notes",
	}
	if got := projectsOf(t, f, root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects\n got %v\nwant %v", got, want)
	}
	for _, name := range []string{"unknown", "Claude app"} {
		if _, ok := f[name]; ok || f.of(name) != (Folder{Path: name, Project: name}) {
			t.Errorf("%s is %+v", name, f.of(name))
		}
	}
}

// TestFindProjectsFollowsLinks: a link to a repository, or a trailing
// slash, is the same folder of the same project.
func TestFindProjectsFollowsLinks(t *testing.T) {
	root := tree(t, "src/app/.git/HEAD=x", "src/app/pkg")
	if err := os.Symlink(filepath.Join(root, "src", "app"), filepath.Join(root, "app")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	app := filepath.Join(root, "src", "app")
	want := map[string]Folder{
		filepath.Join(root, "app", "pkg"):  {Path: filepath.Join(app, "pkg"), Project: app},
		app + string(filepath.Separator):   {Path: app, Project: app},
		filepath.Join(root, "app", "gone"): {Path: filepath.Join(app, "gone"), Project: app},
	}
	var dirs []string
	for d := range want {
		dirs = append(dirs, d)
	}
	f := FindProjects(ledgerIn(dirs...))
	for dir, want := range want {
		if got := f.of(dir); got != want {
			t.Errorf("%s is %+v, want %+v", dir, got, want)
		}
	}
}

// TestFindProjectsClaudeWorktrees: a Claude Code worktree is its
// repository's while it lives, and after it is gone, and after the
// repository is gone too.
func TestFindProjectsClaudeWorktrees(t *testing.T) {
	root := tree(t,
		"src/app/.git/worktrees/fix/commondir=../..",
		"src/app/.claude/worktrees/fix/.git=gitdir: $ROOT/src/app/.git/worktrees/fix",
		"src/app/.claude/worktrees/fix/web",
	)
	dirs := []string{
		"src/app/.claude/worktrees/fix/web", "src/app/.claude/worktrees/old", "src/app/.claude/worktrees/old/web",
		"src/gone/.claude/worktrees/x/y", "src/gone",
	}
	want := []string{"src/app", "src/app", "src/app", "src/gone", "src/gone"}
	if got := projectsOf(t, FindProjects(ledgerIn(under(root, dirs...)...)), root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects\n got %v\nwant %v", got, want)
	}
}

// TestFindProjectsCodexWorktrees: a Codex worktree is its repository's
// while it lives. One that is gone is the one project of its name a .git
// entry found; with none, or two, the gone worktrees of a name are one
// project.
func TestFindProjectsCodexWorktrees(t *testing.T) {
	root := tree(t,
		"src/app/.git/worktrees/app/commondir=../..",
		"home/.codex/worktrees/1a2b/app/.git=gitdir: $ROOT/src/app/.git/worktrees/app",
		"src/site/.git/HEAD=x",
		"old/site/.git/HEAD=x",
	)
	dirs := []string{
		"home/.codex/worktrees/1a2b/app", "home/.codex/worktrees/3c4d/app", "home/.codex/worktrees/5e6f/app/docs",
		"src/site", "old/site", "home/.codex/worktrees/7a8b/site", "home/.codex/worktrees/9c0d/site/api",
		"home/.codex/worktrees/aa11/tool", "home/.codex/worktrees/bb22/tool",
		"home/.codex/worktrees/cc33",
	}
	want := []string{
		"src/app", "src/app", "src/app",
		"src/site", "old/site", "home/.codex/worktrees/*/site", "home/.codex/worktrees/*/site",
		"home/.codex/worktrees/*/tool", "home/.codex/worktrees/*/tool",
		"home/.codex/worktrees/cc33",
	}
	if got := projectsOf(t, FindProjects(ledgerIn(under(root, dirs...)...)), root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects\n got %v\nwant %v", got, want)
	}
}

// TestFindProjectsLeavesPrivateFoldersUnread: nothing inside a folder macOS
// asks about is read, nor reached through a link. A folder there is its own
// project, but for the worktree folders; a worktree whose git folder is
// there is told by where that is.
func TestFindProjectsLeavesPrivateFoldersUnread(t *testing.T) {
	root := tree(t,
		"Documents/app/.git/worktrees/app/commondir=../../../../elsewhere/.git",
		"Documents/app/sub",
		"home/.codex/worktrees/1a2b/app/.git=gitdir: $ROOT/Documents/app/.git/worktrees/app",
		"Documents/gits/x/commondir=$ROOT/elsewhere/.git",
		"wt/.git=gitdir: $ROOT/gits/x",
		"elsewhere/app/.git/HEAD=x",
		"elsewhere/app/sub",
	)
	f := finder{private: []string{filepath.Join(root, "Documents")}}
	dirs := []string{"Documents/app/sub", "Documents/app/.claude/worktrees/x", "home/.codex/worktrees/1a2b/app"}
	want := []string{"Documents/app/sub", "Documents/app", "Documents/app"}
	if got := projectsOf(t, f.find(under(root, dirs...)), root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects\n got %v\nwant %v", got, want)
	}

	// work leads into Documents, where a link that must not be read leads
	// out again; gits leads to a git folder there, whose commondir file
	// must not be read.
	for link, to := range map[string]string{"work": "Documents/work", "Documents/work": "elsewhere", "gits": "Documents/gits"} {
		if err := os.Symlink(filepath.Join(root, to), filepath.Join(root, link)); err != nil {
			t.Skip("no symlinks here:", err)
		}
	}
	dirs = []string{"work/app/sub", "wt"}
	want = []string{"Documents/work/app/sub", "wt"}
	if got := projectsOf(t, f.find(under(root, dirs...)), root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects through links\n got %v\nwant %v", got, want)
	}

	home := filepath.FromSlash("/Users/ann")
	mac := privateFolders("darwin", home)
	for _, d := range []string{"/Volumes", "/Users/ann/Documents", "/Users/ann/Library/Mobile Documents"} {
		if !(finder{private: mac}).inside(filepath.Join(filepath.FromSlash(d), "x")) {
			t.Errorf("%s is not private on macOS: %v", d, mac)
		}
	}
	if (finder{private: mac}).inside(filepath.FromSlash("/Users/ann/Documentsx")) {
		t.Error("a folder beside Documents is private")
	}
	if got := privateFolders("linux", home); got != nil {
		t.Errorf("private folders on Linux: %v", got)
	}
}

// TestFindProjectsInHomeRepository: a home folder that is a repository is
// the project of its own sessions, not of the folders in it.
func TestFindProjectsInHomeRepository(t *testing.T) {
	root := tree(t, "home/.git/HEAD=x", "home/notes", "home/src/app/.git/HEAD=x")
	f := finder{home: filepath.Join(root, "home")}
	dirs := []string{"home", "home/notes", "home/src/gone", "home/src/app/pkg"}
	want := []string{"home", "home/notes", "home/src/gone", "home/src/app"}
	if got := projectsOf(t, f.find(under(root, dirs...)), root, dirs...); !reflect.DeepEqual(got, want) {
		t.Errorf("projects\n got %v\nwant %v", got, want)
	}
}

// TestProjectsMergeFolders: the working folders of one project make one row,
// with their sessions, tokens, and harnesses, and the count of folders; an
// account's projects too.
func TestProjectsMergeFolders(t *testing.T) {
	st := state.NewState()
	add := func(id, provider, dir string, n int64) {
		st.Sessions[state.Key(provider, id)] = &state.Session{Provider: provider, Project: dir, Updated: t0,
			By: map[string]snapshot.Tokens{"ann": tok(n)}}
	}
	add("s1", "claude", "/src/app", 100)
	add("s2", "codex", "/codex/1a2b/app", 300)
	add("s3", "claude", "/src/app/", 10)
	add("s4", "claude", "/home/ann", 5)
	f := Folders{
		"/src/app":        {Path: "/src/app", Project: "/src/app"},
		"/src/app/":       {Path: "/src/app", Project: "/src/app"},
		"/codex/1a2b/app": {Path: "/codex/1a2b/app", Project: "/src/app"},
	}
	var got []string
	for _, p := range Projects(st, f) {
		got = append(got, fmt.Sprintf("%s %d %d %d %s", p.Path, p.Folders, p.Sessions, p.Tokens.Input, strings.Join(p.Providers, ",")))
	}
	if want := []string{"/src/app 2 3 410 codex,claude", "/home/ann 1 1 5 claude"}; !reflect.DeepEqual(got, want) {
		t.Errorf("projects = %q, want %q", got, want)
	}
	got = nil
	for _, a := range Totals(st, f) {
		for _, p := range a.Projects {
			got = append(got, fmt.Sprintf("%s %s %d %d", a.Provider, p.Path, p.Folders, p.Sessions))
		}
	}
	if want := []string{"claude /src/app 1 2", "claude /home/ann 1 1", "codex /src/app 1 1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("accounts' projects = %q, want %q", got, want)
	}
}

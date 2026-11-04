package collect

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neoromantic/ai-usage/internal/state"
)

// TestRepoID: the SSH and HTTPS urls of one repository make one id, with
// no user, port, case, or trailing .git; a local folder's has none.
func TestRepoID(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:acme/api.git":              "github.com/acme/api",
		"https://github.com/acme/api":              "github.com/acme/api",
		"https://github.com/Acme/API.git/":         "github.com/acme/api",
		"https://ann:token@github.com/acme/api":    "github.com/acme/api",
		"ssh://git@github.com:22/acme/api.git":     "github.com/acme/api",
		"ssh://git@github.com/acme/api":            "github.com/acme/api",
		"git://github.com/acme/api.git":            "github.com/acme/api",
		"github.com:/acme/api":                     "github.com/acme/api",
		" git@gitlab.example.com:team/sub/app.git": "gitlab.example.com/team/sub/app",
		"ssh://git@[2001:db8::1]:2222/acme/api":    "2001:db8::1/acme/api",
		"https://example.com/acme/api?x=1#y":       "example.com/acme/api",
		"/srv/git/api.git":                         "",
		"../api":                                   "",
		"file:///srv/git/api.git":                  "",
		`C:\src\api`:                               "",
		"C:/src/api":                               "",
		"https://github.com/":                      "",
		"":                                         "",
	} {
		if got := RepoID(url); got != want {
			t.Errorf("RepoID(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestOriginURL: the url of the remote origin, in either form of its
// section, and no other remote's.
func TestOriginURL(t *testing.T) {
	for config, want := range map[string]string{
		"[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/api.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n": "git@github.com:acme/api.git",
		"[remote \"upstream\"]\n\turl = https://github.com/up/api\n[remote \"origin\"]\n\tURL = \"https://github.com/acme/api\"\n":          "https://github.com/acme/api",
		"[remote.origin]\nurl=https://github.com/acme/api\n":                                                                                "https://github.com/acme/api",
		"[remote \"upstream\"]\n\turl = https://github.com/up/api\n":                                                                        "",
		"[remote \"Origin\"]\n\turl = https://github.com/acme/api\n":                                                                        "",
		"[core]\n\turl = https://github.com/acme/api\n":                                                                                     "",
		"": "",
	} {
		if got := originURL(config); got != want {
			t.Errorf("originURL(%q) = %q, want %q", config, got, want)
		}
	}
}

// TestFindRepos: a repository's worktrees count as it does, so they share
// its origin; a submodule has its own; a bare repository's and a separate
// git folder's config is read; a repository without an origin, or a folder
// outside any, has no id.
func TestFindRepos(t *testing.T) {
	origin := func(url string) string { return "[remote \"origin\"]\n\turl = " + url + "\n" }
	root := tree(t,
		"src/app/.git/HEAD=ref: refs/heads/main",
		"src/app/.git/config="+origin("git@github.com:acme/app.git"),
		"src/app/.git/worktrees/feature/commondir=../..",
		"wt/feature/.git=gitdir: $ROOT/src/app/.git/worktrees/feature\n",
		"src/app/.git/modules/lib/HEAD=ref: refs/heads/main",
		"src/app/.git/modules/lib/config="+origin("https://github.com/acme/lib"),
		"src/app/vendor/lib/.git=gitdir: ../../.git/modules/lib\n",
		"other/app/.git/HEAD=ref: refs/heads/main",
		"other/app/.git/config="+origin("https://github.com/ACME/app.git"),
		"bare/app.git/HEAD=ref: refs/heads/main",
		"bare/app.git/config="+origin("ssh://git@github.com/acme/bare"),
		"bare/app.git/worktrees/main/commondir=../..",
		"bare/main/.git=gitdir: $ROOT/bare/app.git/worktrees/main",
		"hidden/.bare/HEAD=ref: refs/heads/main",
		"hidden/.bare/config="+origin("git@github.com:acme/hidden.git"),
		"hidden/.git=gitdir: ./.bare",
		"store/sep.git/HEAD=ref: refs/heads/main",
		"store/sep.git/config="+origin("git@github.com:acme/sep.git"),
		"sep/.git=gitdir: $ROOT/store/sep.git",
		"local/.git/HEAD=ref: refs/heads/main",
		"local/.git/config="+origin("/srv/git/local.git"),
		"none/.git/HEAD=ref: refs/heads/main",
		"none/.git/config=[core]\n\tbare = false\n",
		"plain/notes",
	)
	dirs := []string{"src/app", "wt/feature", "src/app/vendor/lib", "other/app", "bare/main", "hidden", "sep", "local", "none", "plain/notes"}
	st := ledgerIn(append(under(root, dirs...), "unknown", "Claude app")...)
	r := FindRepos(st, FindProjects(state.Dir(t.TempDir()), st))
	got := map[string]string{}
	for p, id := range r.IDs {
		rel, _ := filepath.Rel(root, p)
		got[filepath.ToSlash(rel)] = id
	}
	want := map[string]string{
		"src/app":            "github.com/acme/app",
		"src/app/vendor/lib": "github.com/acme/lib",
		"other/app":          "github.com/acme/app",
		"bare/app.git":       "github.com/acme/bare",
		"hidden":             "github.com/acme/hidden",
		"sep":                "github.com/acme/sep",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ids\n got %v\nwant %v", got, want)
	}
}

// TestFindReposSkipsPrivateFolders: on macOS a repository in a folder the
// system asks the person about is not read.
func TestFindReposSkipsPrivateFolders(t *testing.T) {
	root := tree(t,
		"Documents/app/.git/HEAD=ref: refs/heads/main",
		"Documents/app/.git/config=[remote \"origin\"]\n\turl = git@github.com:acme/app.git\n",
	)
	f := finder{home: root, private: privateFolders("darwin", root)}
	if id := f.repoID(filepath.Join(root, "Documents", "app")); id != "" {
		t.Errorf("a repository in Documents has id %q", id)
	}
	f.private = nil
	if id := f.repoID(filepath.Join(root, "Documents", "app")); id != "github.com/acme/app" {
		t.Errorf("id = %q", id)
	}
}

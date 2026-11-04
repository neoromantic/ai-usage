package collect

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neoromantic/ai-usage/internal/state"
)

// Repos are the repositories of this device's projects: where each working
// folder counts, and the id of each project whose repository has an origin,
// by the project's folder. Only those projects go to the team, each sealed
// with its repository's key.
type Repos struct {
	Folders Folders
	IDs     map[string]string
}

// FindRepos finds the repository id of each project of the working folders
// in st, which f places. It reads each repository's git config, runs no
// git, and on macOS does not look inside the folders the system asks the
// person about before an app reads them.
func FindRepos(st *state.State, f Folders) Repos {
	home, _ := os.UserHomeDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	fi := finder{home: home, private: privateFolders(runtime.GOOS, home)}
	out := Repos{Folders: f, IDs: map[string]string{}}
	seen := map[string]bool{}
	for _, s := range st.Sessions {
		p := f.of(s.Project).Project
		if seen[p] {
			continue
		}
		seen[p] = true
		if id := fi.repoID(p); id != "" {
			out.IDs[p] = id
		}
	}
	return out
}

// repoID is the id of the origin of the repository whose folder is dir, or
// "" when it has none, or is in a private folder.
func (f finder) repoID(dir string) string {
	if !filepath.IsAbs(dir) || f.inside(dir) {
		return ""
	}
	cfg := f.gitConfig(dir)
	if cfg == "" || f.inside(cfg) {
		return ""
	}
	return RepoID(originURL(readSmall(cfg)))
}

// gitConfig is the config of the repository whose folder is dir: in its
// .git folder; in the git folder a .git file names, or the common one of a
// linked worktree; in .bare, as a bare repository with its worktrees beside
// it keeps it; or dir's own, when dir is a bare repository.
func (f finder) gitConfig(dir string) string {
	dotgit := filepath.Join(dir, ".git")
	if fi, err := os.Stat(dotgit); err == nil {
		if fi.IsDir() {
			return filepath.Join(dotgit, "config")
		}
		line, _, _ := strings.Cut(readSmall(dotgit), "\n")
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if gitdir = strings.TrimSpace(gitdir); !ok || gitdir == "" {
			return ""
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		gitdir = filepath.Clean(gitdir)
		if common := f.commonDir(gitdir); common != "" {
			gitdir = common
		}
		return filepath.Join(f.real(gitdir), "config")
	}
	for _, d := range []string{filepath.Join(dir, ".bare"), dir} {
		if isFile(filepath.Join(d, "HEAD")) && isFile(filepath.Join(d, "config")) {
			return filepath.Join(d, "config")
		}
	}
	return ""
}

// isFile reports whether path is a regular file.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// originURL is the url of the remote origin in the git config text, or "".
func originURL(config string) string {
	origin := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if head, ok := strings.CutPrefix(line, "["); ok {
			head, _, _ = strings.Cut(head, "]")
			name, sub, _ := strings.Cut(strings.TrimSpace(head), " ")
			// [remote "origin"], or the older [remote.origin].
			origin = strings.EqualFold(name, "remote") && strings.Trim(strings.TrimSpace(sub), `"`) == "origin" ||
				strings.EqualFold(head, "remote.origin")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if origin && ok && strings.EqualFold(strings.TrimSpace(key), "url") {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}

// RepoID is a repository's id: the host and path of its remote's url,
// lowercase, without a user, a port, or a trailing .git, so the SSH and
// HTTPS urls of one repository make one id, as github.com/acme/api. A url
// of a local folder, or of a file, has none.
func RepoID(url string) string {
	s := strings.TrimSpace(url)
	var host, path string
	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		if strings.EqualFold(scheme, "file") {
			return ""
		}
		host, path, _ = strings.Cut(rest, "/")
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		host = withoutPort(host)
	} else {
		// [user@]host:path, as scp writes it. A colon after a slash, or a
		// drive letter, is a local folder's.
		colon, slash := strings.Index(s, ":"), strings.IndexAny(s, `/\`)
		if colon < 0 || slash >= 0 && slash < colon {
			return ""
		}
		host, path = s[:colon], s[colon+1:]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if len(host) < 2 {
			return ""
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	path = strings.Trim(strings.ToLower(path), "/")
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

// withoutPort is host without its port, an IPv6 address without brackets.
func withoutPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end > 0 {
			return host[1:end]
		}
		return host
	}
	h, _, _ := strings.Cut(host, ":")
	return h
}

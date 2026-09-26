package collect

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neoromantic/ai-usage/internal/state"
)

// Folder is a session's working folder as the file system names it, and the
// project it counts under.
type Folder struct {
	Path    string
	Project string
}

// Folders maps the working folders the logs name to where they count. A
// folder missing from it is a project of its own.
type Folders map[string]Folder

// of is where the working folder dir counts.
func (f Folders) of(dir string) Folder {
	if x, ok := f[dir]; ok {
		return x
	}
	return Folder{Path: dir, Project: dir}
}

// FindProjects finds the project of each working folder in the ledger, so
// that a repository's worktrees and subfolders make one project. It looks
// at the file system, runs no git, and needs none of the folders to exist:
//
//   - A folder's project is the nearest folder at or above it with a .git
//     entry: a .git folder, or a .git file that names a git folder of its
//     own, as a submodule's does. The home folder is only its own.
//   - A linked worktree, whose .git file names a folder in
//     <common>/worktrees, is its repository's: the folder that holds the
//     common git folder when that is .git, or hidden as .bare is; else the
//     folder the common git folder's config names as its worktree, as a
//     submodule's does, or whose .git file names it; else the bare
//     repository itself.
//   - Claude Code keeps worktrees in <repo>/.claude/worktrees/<name>. One
//     with no .git entry left counts as <repo> does.
//   - Codex keeps them in ~/.codex/worktrees/<id>/<name>, named after their
//     repository. One with no .git entry left is the project named <name>
//     that a .git entry found, when there is exactly one. Else the Codex
//     worktrees of one name make one project, ~/.codex/worktrees/*/<name>.
//   - Any other folder, such as the home folder, is its own project.
//
// Links are resolved and paths cleaned, so neither splits a project. On
// macOS it does not look inside the folders the system asks the person
// about before an app reads them, nor follow a link into one: a folder
// there is its own project, but for the worktree folders above.
func FindProjects(st *state.State) Folders {
	home, _ := os.UserHomeDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	dirs := make([]string, 0, len(st.Sessions))
	for _, s := range st.Sessions {
		dirs = append(dirs, s.Project)
	}
	return finder{home: home, private: privateFolders(runtime.GOOS, home)}.find(dirs)
}

// privateFolders are the folders macOS asks the person about before an app
// reads inside them: Desktop, Documents, Downloads, iCloud Drive and other
// cloud storage, other apps' data, and other volumes.
func privateFolders(goos, home string) []string {
	if goos != "darwin" {
		return nil
	}
	out := []string{filepath.Join("/", "Volumes")}
	if home != "" {
		for _, d := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents", "Library/CloudStorage",
			"Library/Containers", "Library/Group Containers"} {
			out = append(out, filepath.Join(home, d))
		}
	}
	return out
}

// finder finds projects without looking inside its private folders. Its
// home folder, with its links resolved, is no project of the folders in it.
type finder struct {
	home    string
	private []string
}

// place is where a folder counts: its project, whether a .git entry found
// it, the git folder of its own that a .git file there names, and for a
// Codex worktree none found, the worktree's name.
type place struct {
	project string
	git     bool
	gitdir  string
	name    string
}

// find is where each of dirs counts.
func (f finder) find(dirs []string) Folders {
	out := Folders{}
	places := map[string]place{}
	// owners are the folders whose .git file names a git folder of their
	// own, by that folder: a linked worktree of such a repository is theirs.
	owners := map[string]string{}
	for _, dir := range dirs {
		// A project that is no path, as unknown or Claude app, stays as it is.
		if _, ok := out[dir]; ok || !filepath.IsAbs(dir) {
			continue
		}
		path := f.real(dir)
		pl := f.place(path)
		places[dir], out[dir] = pl, Folder{Path: path, Project: pl.project}
		if pl.gitdir != "" {
			owners[pl.gitdir] = pl.project
		}
	}
	// named are the projects .git entries found, by their folder's name.
	named := map[string]map[string]bool{}
	for dir, pl := range places {
		if o, ok := owners[pl.project]; ok {
			pl.project = o
			places[dir], out[dir] = pl, Folder{Path: out[dir].Path, Project: o}
		}
		if pl.git {
			n := filepath.Base(pl.project)
			if named[n] == nil {
				named[n] = map[string]bool{}
			}
			named[n][pl.project] = true
		}
	}
	for dir, pl := range places {
		if pl.name == "" || len(named[pl.name]) != 1 {
			continue
		}
		for p := range named[pl.name] {
			out[dir] = Folder{Path: out[dir].Path, Project: p}
		}
	}
	return out
}

// place is where the folder at path counts, before a Codex worktree is
// matched to its repository by name.
func (f finder) place(path string) place {
	wt, ok := worktreeOf(path)
	if !f.inside(path) {
		for d := path; ; d = filepath.Dir(d) {
			// A home folder that is a repository, as of its settings, holds
			// the folders in it but is not their project.
			if d == f.home && d != path {
				break
			}
			if pl, found := f.gitProject(d); found {
				return pl
			}
			if (ok && d == wt.top) || filepath.Dir(d) == d {
				break
			}
		}
	}
	switch {
	case !ok:
		return place{project: path}
	case wt.repo != "":
		return f.place(wt.repo)
	default:
		return place{project: wt.glob, name: wt.name}
	}
}

// gitProject is where dir counts when it has a .git entry: dir itself, or
// for a linked worktree, its repository's folder.
func (f finder) gitProject(dir string) (place, bool) {
	dotgit := filepath.Join(dir, ".git")
	fi, err := os.Stat(dotgit)
	if err != nil {
		return place{}, false
	}
	own := place{project: dir, git: true}
	if fi.IsDir() {
		return own, true
	}
	line, _, _ := strings.Cut(readSmall(dotgit), "\n")
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if gitdir = strings.TrimSpace(gitdir); !ok || gitdir == "" {
		return own, true
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	common := f.commonDir(filepath.Clean(gitdir))
	if common == "" {
		own.gitdir = f.real(gitdir)
		return own, true
	}
	common = f.real(common)
	if strings.HasPrefix(filepath.Base(common), ".") {
		return place{project: filepath.Dir(common), git: true}, true
	}
	if w := f.workTree(common); w != "" {
		return place{project: w, git: true}, true
	}
	return place{project: common, git: true}, true
}

// commonDir is the git folder the linked worktree whose git folder is
// gitdir shares with its repository, or "" when gitdir is no linked
// worktree's. Git names it in gitdir's commondir file; a worktree whose
// gitdir is gone, or in a private folder, is told by where gitdir was.
func (f finder) commonDir(gitdir string) string {
	if !f.inside(f.real(gitdir)) {
		line, _, _ := strings.Cut(readSmall(filepath.Join(gitdir, "commondir")), "\n")
		if c := strings.TrimSpace(line); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitdir, c)
			}
			return filepath.Clean(c)
		}
		if _, err := os.Stat(gitdir); err == nil {
			return ""
		}
	}
	if filepath.Base(filepath.Dir(gitdir)) == "worktrees" {
		return filepath.Dir(filepath.Dir(gitdir))
	}
	return ""
}

// workTree is the folder that the config in the git folder common names as
// its worktree, as core.worktree, with its links resolved; or "" when it
// names none, as a bare repository's does not.
func (f finder) workTree(common string) string {
	if f.inside(common) {
		return ""
	}
	section := ""
	for _, line := range strings.Split(readSmall(filepath.Join(common, "config")), "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "["); ok {
			name, _, _ = strings.Cut(name, "]")
			section = strings.ToLower(strings.TrimSpace(name))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "core" || !strings.EqualFold(strings.TrimSpace(key), "worktree") {
			continue
		}
		if value = strings.Trim(strings.TrimSpace(value), `"`); value == "" {
			return ""
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(common, value)
		}
		return f.real(value)
	}
	return ""
}

// real is p cleaned, with its links resolved as far as it exists outside
// the private folders.
func (f finder) real(p string) string {
	p = filepath.Clean(p)
	if len(f.private) > 0 {
		return f.follow(p)
	}
	rest := ""
	for d := p; ; {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		up := filepath.Dir(d)
		if up == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = up
	}
}

// follow is the clean absolute path p with its links resolved one at a
// time, as far as it exists. It reads nothing in a private folder: the rest
// of a path that reaches one, as written or through a link, stays as it is.
func (f finder) follow(p string) string {
	sep := string(filepath.Separator)
	done, todo := sep, strings.Split(p, sep)
	for links := 0; len(todo) > 0; {
		if todo[0] == "" || todo[0] == "." {
			todo = todo[1:]
			continue
		}
		next := filepath.Join(done, todo[0])
		rest := filepath.Join(append([]string{next}, todo[1:]...)...)
		if f.inside(next) {
			return rest
		}
		fi, err := os.Lstat(next)
		if err != nil {
			return rest
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			done, todo = next, todo[1:]
			continue
		}
		link, err := os.Readlink(next)
		if links++; err != nil || links > 255 {
			return rest
		}
		if filepath.IsAbs(link) {
			done = sep
		}
		todo = append(strings.Split(link, sep), todo[1:]...)
	}
	return done
}

// inside reports whether p is a private folder or in one.
func (f finder) inside(p string) bool {
	for _, d := range f.private {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// worktree is a worktree folder a tool keeps: top. Claude Code keeps it in
// repo; Codex under its home, with its repository's name, and the Codex
// worktrees of that name are glob.
type worktree struct {
	top, repo  string
	name, glob string
}

// worktreeOf finds the innermost tool's worktree folder that holds path.
func worktreeOf(path string) (worktree, bool) {
	for d := path; filepath.Dir(d) != d; d = filepath.Dir(d) {
		up := filepath.Dir(d)
		up2 := filepath.Dir(up)
		switch {
		case filepath.Base(up) == "worktrees" && filepath.Base(up2) == ".claude":
			return worktree{top: d, repo: filepath.Dir(up2)}, true
		case filepath.Base(up2) == "worktrees" && filepath.Base(filepath.Dir(up2)) == ".codex":
			name := filepath.Base(d)
			return worktree{top: d, name: name, glob: filepath.Join(up2, "*", name)}, true
		}
	}
	return worktree{}, false
}

// readSmall is the start of a small regular file, or "" when there is no
// such file. A .git file or a commondir file is one line, and a git
// config's core section comes first.
func readSmall(path string) string {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	b, _ := io.ReadAll(io.LimitReader(fh, 16<<10))
	return string(b)
}

// Package selfupdate replaces the collector binary with the newest GitHub
// release. The run that finds a release finishes on the old binary; the new
// one is in place for the next run. There is no switch to turn it off.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Repo is where releases are published.
const Repo = "neoromantic/ai-usage"

// Updater checks and installs releases.
type Updater struct {
	Current string
	// Exe is the binary to replace. It defaults to the running executable.
	Exe string
	// GitHub is the site the releases are on, https://github.com by default.
	GitHub  string
	HTTP    *http.Client
	GOOS    string
	GOARCH  string
	MaxSize int64
	// Lookup bounds the request that finds the latest release, so a slow
	// GitHub holds up a run only this long.
	Lookup time.Duration
	// Stall ends a download that receives nothing for this long. There is
	// no limit on the whole download: the caller's context bounds it, so a
	// slow link can still finish.
	Stall time.Duration
	// Skip is a release that an earlier check downloaded and could not
	// install. Check does not download it again: it reports it as the latest
	// with ErrSkipped.
	Skip string
}

// ErrSkipped is Check's answer when the latest release is Updater.Skip.
var ErrSkipped = errors.New("the latest release did not install at an earlier check")

var (
	errStalled  = errors.New("download stalled")
	errSlow     = errors.New("lookup timed out")
	errTooLarge = errors.New("update check: download is larger than expected")
)

// Pages on the site: a repository's releases/latest, which a repository that
// was renamed or moved redirects to under its new name; a release's page,
// where releases/latest redirects; and the releases page, where it redirects
// while there is no release. Owner and repository names on GitHub use only
// these characters, in any case.
var (
	latestPage   = regexp.MustCompile(`^/[\w.-]+/[\w.-]+/releases/latest$`)
	tagPage      = regexp.MustCompile(`^/([\w.-]+/[\w.-]+)/releases/tag/([^/]+)/?$`)
	releasesPage = regexp.MustCompile(`^/[\w.-]+/[\w.-]+/releases/?$`)
)

// maxMoves is how many times the repository can have moved on the way to
// its latest release.
const maxMoves = 3

// AssetName is the release file for an OS and architecture.
func AssetName(goos, goarch string) string {
	name := "ai-usage_" + goos + "_" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Result says what a check found.
type Result struct {
	Latest    string
	Installed bool
	// Downloaded says the check downloaded the release's binary, whether or
	// not it then installed it.
	Downloaded bool
}

func (u *Updater) fill() error {
	if u.GitHub == "" {
		u.GitHub = "https://github.com"
	}
	if u.HTTP == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = time.Minute
		u.HTTP = &http.Client{Transport: t}
	}
	if u.Lookup == 0 {
		u.Lookup = 30 * time.Second
	}
	if u.Stall == 0 {
		u.Stall = time.Minute
	}
	if u.GOOS == "" {
		u.GOOS = runtime.GOOS
	}
	if u.GOARCH == "" {
		u.GOARCH = runtime.GOARCH
	}
	if u.MaxSize == 0 {
		u.MaxSize = 200 << 20
	}
	if u.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		u.Exe = exe
	}
	return nil
}

// Dev reports whether version is a development build, which never updates.
func Dev(version string) bool {
	_, ok := parse(version)
	return !ok
}

// Check looks up the latest release and installs it when it is newer.
func (u *Updater) Check(ctx context.Context) (Result, error) {
	if Dev(u.Current) {
		return Result{}, errors.New("development build; self-update applies to release builds")
	}
	if err := u.fill(); err != nil {
		return Result{}, err
	}
	cleanupOld(u.Exe)

	repo, tag, err := u.latest(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Latest: tag}
	if !Newer(tag, u.Current) {
		return res, nil
	}
	// A process that runs for long, such as the interactive view, can find a
	// release that a scheduled run has already put in its place.
	if v, err := reports(ctx, u.Exe); err == nil && !Dev(v) && !Newer(tag, v) {
		res.Installed = true
		return res, nil
	}
	if tag == u.Skip {
		return res, ErrSkipped
	}
	want := AssetName(u.GOOS, u.GOARCH)
	// A binary in a place this user cannot write, such as a system bin
	// directory, would download every release and then fail to install it.
	if err := writable(filepath.Dir(u.Exe)); err != nil {
		return res, fmt.Errorf("cannot write beside %s: %w", u.Exe, err)
	}
	sums, err := u.download(ctx, repo, tag, "checksums.txt", 1<<20)
	if err != nil {
		return res, err
	}
	wantSum, ok := checksum(sums, want)
	if !ok {
		return res, fmt.Errorf("checksums.txt of release %s does not list %s", tag, want)
	}
	bin, err := u.download(ctx, repo, tag, want, u.MaxSize)
	// A binary larger than the limit was downloaded as far as the limit.
	res.Downloaded = err == nil || errors.Is(err, errTooLarge)
	if err != nil {
		return res, err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != wantSum {
		return res, fmt.Errorf("%s does not match its checksum", want)
	}
	tmp, err := stage(u.Exe, bin, u.GOOS)
	if err != nil {
		return res, err
	}
	// The checksum proves the download is what was uploaded, not that it
	// starts on this machine. A binary that cannot start would never run the
	// update that replaces it.
	if err := starts(ctx, tmp, tag); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("release %s does not run here: %w", tag, err)
	}
	if err := swap(u.Exe, tmp, u.GOOS); err != nil {
		_ = os.Remove(tmp)
		return res, err
	}
	res.Installed = true
	return res, nil
}

// latest is the latest release's tag, read from where releases/latest
// redirects: releases/tag/<tag>. GitHub skips drafts and prereleases there,
// as its API does, and redirects to the releases page while there is no
// release. The page is read rather than the API, which allows 60 requests an
// hour per address without a token: ten devices behind one address that
// check at every run would come close to that. repo is the repository the
// release is in, which is Repo unless that was renamed or moved.
func (u *Updater) latest(ctx context.Context) (repo, tag string, err error) {
	page := u.GitHub + "/" + Repo + "/releases/latest"
	ctx, cancel := context.WithTimeoutCause(ctx, u.Lookup, errSlow)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "ai-usage/"+u.Current)
	// The redirect to the release is the answer, so it is not followed. A
	// redirect to releases/latest under a new name, on the same site, is.
	client := *u.HTTP
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) <= maxMoves && next.URL.Host == req.URL.Host && latestPage.MatchString(next.URL.Path) {
			return nil
		}
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(context.Cause(ctx), errSlow) {
			return "", "", fmt.Errorf("update check: %s did not answer in %s", req.URL.Host, u.Lookup)
		}
		return "", "", fmt.Errorf("update check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("update check: HTTP %d from %s%s", resp.StatusCode, req.URL.Host, reason(resp.Body))
	}
	if resp.StatusCode < 300 {
		return "", "", fmt.Errorf("update check: HTTP %d from %s, not a redirect to the latest release", resp.StatusCode, req.URL.Host)
	}
	// A relative Location is resolved against the request.
	loc, err := resp.Location()
	if err != nil {
		return "", "", fmt.Errorf("update check: HTTP %d from %s with no usable Location: %w", resp.StatusCode, req.URL.Host, err)
	}
	if loc.Host == req.URL.Host {
		if m := tagPage.FindStringSubmatch(loc.Path); m != nil {
			return m[1], m[2], nil
		}
		if releasesPage.MatchString(loc.Path) {
			return "", "", fmt.Errorf("update check: %s has no release", strings.TrimSuffix(loc.String(), "/"))
		}
	}
	return "", "", fmt.Errorf("update check: %s redirects to %s, not to a release", page, loc)
}

// stage writes the new binary beside the old one, where a rename can swap it
// in. On Windows it needs the .exe suffix to run.
func stage(exe string, bin []byte, goos string) (string, error) {
	pattern := ".ai-usage-new-*"
	if goos == "windows" {
		pattern += ".exe"
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), pattern)
	if err != nil {
		return "", fmt.Errorf("cannot write beside %s: %w", exe, err)
	}
	_, werr := tmp.Write(bin)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// reports is the version a binary gives from its version command, which
// touches no state.
func reports(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	return strings.TrimSpace(string(out)), err
}

// starts runs the staged binary and requires it to report the release it
// came from.
func starts(ctx context.Context, bin, tag string) error {
	v, err := reports(ctx, bin)
	if err != nil {
		return err
	}
	got, ok1 := parse(v)
	want, ok2 := parse(tag)
	if !ok1 || !ok2 || got != want {
		return fmt.Errorf("it reports version %q", v)
	}
	return nil
}

// swap puts the staged binary in place of exe. Windows cannot overwrite a
// running executable, so the old one is moved aside first and removed by a
// later run. One moved aside before can still be running, as a long
// `ai-usage schedule run` is; it cannot be removed or replaced, but it can
// be moved again.
func swap(exe, tmp, goos string) error {
	if goos == "windows" {
		old := exe + ".old"
		if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(old, fmt.Sprintf("%s.old-%d", exe, time.Now().UnixNano()))
		}
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp, exe)
}

// cleanupOld removes the binaries earlier updates moved aside. The folder is
// listed rather than globbed, since its path may hold [ or ].
func cleanupOld(exe string) {
	_ = os.Remove(exe + ".old")
	entries, _ := os.ReadDir(filepath.Dir(exe))
	prefix := filepath.Base(exe) + ".old-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(filepath.Dir(exe), e.Name()))
		}
	}
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".ai-usage-check-*")
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return pe.Err
		}
		return err
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

func checksum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// download fetches the file name of release tag from the repository's
// releases/download/<tag>/, which GitHub redirects to where the file is
// stored. The installers fetch the same files through
// releases/latest/download/, which GitHub redirects here.
func (u *Updater) download(ctx context.Context, repo, tag, name string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(u.Stall, func() { cancel(errStalled) })
	defer stall.Stop()
	file := u.GitHub + "/" + repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, file, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ai-usage/"+u.Current)
	failed := func(err error) error {
		if errors.Is(context.Cause(ctx), errStalled) {
			return fmt.Errorf("update check: %s sent nothing for %s", req.URL.Host, u.Stall)
		}
		return fmt.Errorf("update check: %w", err)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, failed(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("release %s has no %s", tag, name)
	}
	if resp.StatusCode != http.StatusOK {
		why := ""
		if resp.StatusCode >= 400 {
			why = reason(resp.Body)
		}
		return nil, fmt.Errorf("update check: HTTP %d from %s%s", resp.StatusCode, resp.Request.URL.Host, why)
	}
	b, err := io.ReadAll(io.LimitReader(progress{resp.Body, stall, u.Stall}, limit+1))
	if err != nil {
		return nil, failed(err)
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// maxReason is the most of a refusal's reason an error keeps.
const maxReason = 120

var (
	// titled is an HTML page's title, or the message of an XML error, as a
	// file store sends.
	titled = regexp.MustCompile(`(?is)<(?:title|message)(?:\s[^>]*)?>([^<]*)<`)
	// addrLike is a run of characters an IP address, with or without a port,
	// is made of.
	addrLike = regexp.MustCompile(`[0-9A-Fa-f.:]*[.:][0-9A-Fa-f.:]*`)
)

// reason is ": " and why a refusal says it refused, from the start of its
// body: a JSON error's message, as GitHub's API sends, an HTML page's title,
// or the text, on one line. An IP address in it, such as the one GitHub
// names when this address is over its limit, is left out, since the team
// sees the error. It is empty when the body says nothing readable.
func reason(body io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(body, 16<<10))
	var j struct {
		Message string `json:"message"`
	}
	s := ""
	switch m := titled.FindSubmatch(b); {
	case json.Unmarshal(b, &j) == nil:
		s = j.Message
	case m != nil:
		s = html.UnescapeString(string(m[1]))
	case !bytes.HasPrefix(bytes.TrimSpace(b), []byte("<")):
		s = string(b)
	}
	s = WithoutAddresses(strings.Join(strings.FieldsFunc(strings.ToValidUTF8(s, ""), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " "))
	if len(s) > maxReason {
		cut := maxReason - len("…")
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	if s == "" {
		return ""
	}
	return ": " + s
}

// WithoutAddresses is s with "(IP address)" in place of each IP address in
// it, with or without a port, such as the one GitHub names when this
// address is over its limit, or the two a network error names. An address
// joined to a word, as in "IP:203.0.113.7", starts after its colon or dot.
func WithoutAddresses(s string) string {
	return addrLike.ReplaceAllStringFunc(s, func(m string) string {
		for i := range len(m) {
			if i > 0 && m[i-1] != ':' && m[i-1] != '.' {
				continue
			}
			a := strings.TrimRight(m[i:], ".:")
			if _, err := netip.ParseAddr(a); err != nil {
				if _, err := netip.ParseAddrPort(a); err != nil {
					continue
				}
			}
			return m[:i] + "(IP address)" + m[i+len(a):]
		}
		return m
	})
}

// progress pushes the stall deadline back whenever bytes arrive.
type progress struct {
	r     io.Reader
	stall *time.Timer
	after time.Duration
}

func (p progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.stall.Reset(p.after)
	}
	return n, err
}

// Newest is the latest release of versions; empty when none is a release.
func Newest(versions ...string) string {
	best := ""
	for _, v := range versions {
		if !Dev(v) && (best == "" || Newer(v, best)) {
			best = v
		}
	}
	return best
}

// Newer reports whether tag is a later version than cur. Both look like v1.2.3.
func Newer(tag, cur string) bool {
	a, ok1 := parse(tag)
	b, ok2 := parse(cur)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return out, false // pre-releases and dirty builds do not take part
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

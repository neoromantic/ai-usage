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
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "neoromantic/ai-usage"

// Updater checks and installs releases.
type Updater struct {
	Current string
	// Exe is the binary to replace. It defaults to the running executable.
	Exe     string
	API     string // defaults to https://api.github.com
	HTTP    *http.Client
	GOOS    string
	GOARCH  string
	MaxSize int64
	// Stall ends a download that receives nothing for this long. There is
	// no limit on the whole download: the caller's context bounds it, so a
	// slow link can still finish.
	Stall time.Duration
}

var errStalled = errors.New("download stalled")

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
}

type release struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (u *Updater) fill() error {
	if u.API == "" {
		u.API = "https://api.github.com"
	}
	if u.HTTP == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = time.Minute
		u.HTTP = &http.Client{Transport: t}
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

	var rel release
	if err := u.getJSON(ctx, u.API+"/repos/"+Repo+"/releases/latest", &rel); err != nil {
		return Result{}, err
	}
	res := Result{Latest: rel.TagName}
	if !Newer(rel.TagName, u.Current) {
		return res, nil
	}
	want := AssetName(u.GOOS, u.GOARCH)
	var binURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			binURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if binURL == "" {
		return res, fmt.Errorf("release %s has no %s", rel.TagName, want)
	}
	if sumsURL == "" {
		return res, fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	// A binary in a place this user cannot write, such as a system bin
	// directory, would download every release and then fail to install it.
	if err := writable(filepath.Dir(u.Exe)); err != nil {
		return res, fmt.Errorf("cannot write beside %s: %w", u.Exe, err)
	}
	sums, err := u.get(ctx, sumsURL, 1<<20)
	if err != nil {
		return res, err
	}
	wantSum, ok := checksum(sums, want)
	if !ok {
		return res, fmt.Errorf("checksums.txt does not list %s", want)
	}
	bin, err := u.get(ctx, binURL, u.MaxSize)
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
	if err := starts(ctx, tmp, rel.TagName); err != nil {
		_ = os.Remove(tmp)
		return res, fmt.Errorf("release %s does not run here: %w", rel.TagName, err)
	}
	if err := swap(u.Exe, tmp, u.GOOS); err != nil {
		_ = os.Remove(tmp)
		return res, err
	}
	res.Installed = true
	return res, nil
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

// starts runs the staged binary's version command, which touches no state,
// and requires it to report the release it came from.
func starts(ctx context.Context, bin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return err
	}
	v := strings.TrimSpace(string(out))
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

func (u *Updater) getJSON(ctx context.Context, url string, v any) error {
	b, err := u.get(ctx, url, 4<<20)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("release list: %w", err)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(u.Stall, func() { cancel(errStalled) })
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update check: HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	b, err := io.ReadAll(io.LimitReader(progress{resp.Body, stall, u.Stall}, limit+1))
	if err != nil {
		return nil, failed(err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("update check: download is larger than expected")
	}
	return b, nil
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

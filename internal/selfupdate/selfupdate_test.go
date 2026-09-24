package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for a release binary: run as
// "<binary> version" with AIU_FAKE_RELEASE set, it prints that version.
func TestMain(m *testing.M) {
	if v := os.Getenv("AIU_FAKE_RELEASE"); v != "" && len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runnable is a binary that starts on this machine and reports version v.
func runnable(t *testing.T, v string) []byte {
	t.Helper()
	t.Setenv("AIU_FAKE_RELEASE", v)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// releases is the path of the repository's releases on the site.
const releases = "/" + Repo + "/releases"

// fakeGitHub serves one latest release as github.com does: releases/latest
// redirects to the release's tag, and a file under releases/download/<tag>/
// redirects to where it is stored.
type fakeGitHub struct {
	tag    string
	assets map[string][]byte
	// status answers a path with an HTTP error.
	status map[string]int
	// pace sends a binary in eight pieces with this pause between them.
	pace time.Duration
	// hang stops sending a binary after its first piece.
	hang bool

	mu     sync.Mutex
	hits   map[string]int
	agents []string
}

func (f *fakeGitHub) start(t *testing.T) *httptest.Server {
	t.Helper()
	f.hits = map[string]int{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		f.agents = append(f.agents, r.UserAgent())
		f.mu.Unlock()
		if code := f.status[r.URL.Path]; code != 0 {
			http.Error(w, "nope", code)
			return
		}
		switch {
		case r.URL.Path == releases+"/latest":
			http.Redirect(w, r, srv.URL+releases+"/tag/"+f.tag, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, releases+"/download/"+f.tag+"/"):
			name := path.Base(r.URL.Path)
			if _, ok := f.assets[name]; !ok {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, "/storage/"+name, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/storage/"):
			name := strings.TrimPrefix(r.URL.Path, "/storage/")
			b, ok := f.assets[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if name == "checksums.txt" || (f.pace == 0 && !f.hang) {
				_, _ = w.Write(b)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			piece := len(b)/8 + 1
			for len(b) > 0 {
				n := min(piece, len(b))
				_, _ = w.Write(b[:n])
				w.(http.Flusher).Flush()
				b = b[n:]
				if f.hang {
					<-r.Context().Done()
					return
				}
				time.Sleep(f.pace)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeGitHub) downloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for p, c := range f.hits {
		if strings.HasPrefix(p, "/storage/") && !strings.HasSuffix(p, "checksums.txt") {
			n += c
		}
	}
	return n
}

// resum rewrites checksums.txt after an asset changed.
func (f *fakeGitHub) resum() {
	delete(f.assets, "checksums.txt")
	f.assets["checksums.txt"] = []byte(sumsFor(f.assets))
}

func sumsFor(assets map[string][]byte) string {
	var b strings.Builder
	for name, body := range assets {
		sum := sha256.Sum256(body)
		b.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	return b.String()
}

// newRelease is a v1.3.0 release with a linux/amd64 binary and its checksums.
func newRelease(bin []byte) *fakeGitHub {
	assets := map[string][]byte{"ai-usage_linux_amd64": bin, "ai-usage_darwin_arm64": []byte("other")}
	assets["checksums.txt"] = []byte(sumsFor(assets))
	return &fakeGitHub{tag: "v1.3.0", assets: assets}
}

// installed writes the running binary into its own directory.
func installed(t *testing.T, name string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(exe, []byte("old binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe
}

func updater(srv *httptest.Server, exe, current string) *Updater {
	return &Updater{Current: current, Exe: exe, GitHub: srv.URL, HTTP: srv.Client(), GOOS: "linux", GOARCH: "amd64"}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// onlyFiles fails when anything but names is left in dir, such as a temp file.
func onlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("directory holds %v, want %v", got, names)
	}
}

func TestCheckInstallsNewerRelease(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	gh := newRelease(bin)
	srv := gh.start(t)
	exe := installed(t, "ai-usage")

	res, err := updater(srv, exe, "v1.2.9").Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Latest: "v1.3.0", Installed: true}) {
		t.Fatalf("Check = %+v", res)
	}
	if read(t, exe) != string(bin) {
		t.Fatal("binary was not replaced")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(exe)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
		}
	}
	onlyFiles(t, filepath.Dir(exe), "ai-usage")
	for _, ua := range gh.agents {
		if !strings.HasPrefix(ua, "ai-usage/") {
			t.Fatalf("User-Agent = %q", ua)
		}
	}
}

func TestCheckLeavesCurrentAlone(t *testing.T) {
	for _, tc := range []struct{ name, tag, current string }{
		{"same", "v1.3.0", "v1.3.0"},
		{"older", "v1.2.0", "v1.3.0"},
		{"pre-release", "v2.0.0-rc.1", "v1.3.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := newRelease([]byte("new binary"))
			gh.tag = tc.tag
			srv := gh.start(t)
			exe := installed(t, "ai-usage")
			res, err := updater(srv, exe, tc.current).Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Installed || res.Latest != tc.tag {
				t.Fatalf("Check = %+v", res)
			}
			if gh.downloads() != 0 {
				t.Fatal("downloaded a release that is not newer")
			}
			if read(t, exe) != "old binary" {
				t.Fatal("binary changed")
			}
		})
	}
}

func TestCheckRefusesBadReleases(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *fakeGitHub, *Updater)
		// want is what the error must name: the release and the missing file,
		// the HTTP code, the version the binary reported, or the stall limit.
		want []string
	}{
		{"no binary for this platform", func(t *testing.T, f *fakeGitHub, u *Updater) { u.GOARCH = "riscv64" }, []string{"v1.3.0", "ai-usage_linux_riscv64"}},
		// checksums.txt lists the binary, but the release has no such file.
		{"binary missing", func(t *testing.T, f *fakeGitHub, u *Updater) { delete(f.assets, "ai-usage_linux_amd64") }, []string{"v1.3.0", "ai-usage_linux_amd64"}},
		{"no checksums", func(t *testing.T, f *fakeGitHub, u *Updater) { delete(f.assets, "checksums.txt") }, []string{"v1.3.0", "checksums.txt"}},
		{"checksums skip the binary", func(t *testing.T, f *fakeGitHub, u *Updater) {
			f.assets["checksums.txt"] = []byte(sumsFor(map[string][]byte{"ai-usage_darwin_arm64": []byte("other")}))
		}, []string{"checksums.txt"}},
		{"checksum mismatch", func(t *testing.T, f *fakeGitHub, u *Updater) {
			f.assets["ai-usage_linux_amd64"] = []byte("tampered binary")
		}, []string{"checksum"}},
		{"oversize binary", func(t *testing.T, f *fakeGitHub, u *Updater) { u.MaxSize = int64(len(bin) - 1) }, []string{"larger"}},
		{"binary download fails", func(t *testing.T, f *fakeGitHub, u *Updater) {
			f.status = map[string]int{"/storage/ai-usage_linux_amd64": http.StatusBadGateway}
		}, []string{"HTTP 502"}},
		// Checksums prove the download is what was uploaded, not that it
		// starts here: a wrong build must not replace a working binary.
		{"binary does not start", func(t *testing.T, f *fakeGitHub, u *Updater) {
			f.assets["ai-usage_linux_amd64"] = []byte("not a program")
			f.resum()
		}, []string{"does not run"}},
		{"binary reports another version", func(t *testing.T, f *fakeGitHub, u *Updater) {
			t.Setenv("AIU_FAKE_RELEASE", "v1.2.9")
		}, []string{"v1.2.9"}},
		{"download stalls", func(t *testing.T, f *fakeGitHub, u *Updater) {
			f.hang, u.Stall = true, 100*time.Millisecond
		}, []string{"100ms"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := newRelease(bin)
			srv := gh.start(t)
			exe := installed(t, "ai-usage")
			u := updater(srv, exe, "v1.2.9")
			tc.setup(t, gh, u)
			res, err := u.Check(context.Background())
			if err == nil {
				t.Fatalf("Check = %+v, want an error naming %q", res, tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("Check error = %v, want it to name %q", err, w)
				}
			}
			if res.Installed {
				t.Fatal("reported an install")
			}
			if read(t, exe) != "old binary" {
				t.Fatal("binary changed")
			}
			onlyFiles(t, filepath.Dir(exe), "ai-usage")
		})
	}
}

// The latest release is the tag releases/latest redirects to, and the
// redirect is not followed. Any other answer is an error that says what came
// back.
func TestLatestRelease(t *testing.T) {
	tag := releases + "/tag/v1.3.0"
	for _, tc := range []struct {
		name string
		code int
		// location is the Location header; SRV stands for the server's URL.
		location string
		// want is the tag found, or what the error must name.
		want string
		ok   bool
	}{
		{"absolute", http.StatusFound, "SRV" + tag, "v1.3.0", true},
		{"relative to the site", http.StatusFound, tag, "v1.3.0", true},
		{"relative to the request", http.StatusMovedPermanently, "tag/v1.3.0", "v1.3.0", true},
		{"no release", http.StatusFound, "SRV" + releases, "has no release", false},
		{"elsewhere on the site", http.StatusFound, "/login", "/login", false},
		{"a page under a release", http.StatusFound, tag + "/assets", "/assets", false},
		{"another site", http.StatusFound, "https://example.invalid" + tag, "example.invalid", false},
		{"no Location", http.StatusFound, "", "Location", false},
		{"no redirect", http.StatusOK, "", "HTTP 200", false},
		{"rate limited", http.StatusTooManyRequests, "", "HTTP 429", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != releases+"/latest" {
					t.Errorf("followed the redirect to %s", r.URL)
					return
				}
				if tc.location != "" {
					w.Header().Set("Location", strings.ReplaceAll(tc.location, "SRV", srv.URL))
				}
				w.WriteHeader(tc.code)
			}))
			t.Cleanup(srv.Close)
			res, err := updater(srv, installed(t, "ai-usage"), "v1.3.0").Check(context.Background())
			switch {
			case tc.ok && (err != nil || res != Result{Latest: tc.want}):
				t.Fatalf("Check = %+v, %v, want %s", res, err, tc.want)
			case !tc.ok && (err == nil || !strings.Contains(err.Error(), tc.want) || res.Latest != ""):
				t.Fatalf("Check = %+v, %v, want an error naming %q", res, err, tc.want)
			}
		})
	}
}

// A slow GitHub holds up a run only as long as Lookup.
func TestLookupTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	u := updater(srv, installed(t, "ai-usage"), "v1.2.9")
	u.Lookup = 100 * time.Millisecond
	start := time.Now()
	_, err := u.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "100ms") {
		t.Fatalf("Check error = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the lookup took %s", took)
	}
}

// A binary in a directory this user cannot write reports why and does not
// download a release it cannot install.
func TestCheckReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	gh := newRelease([]byte("new binary"))
	srv := gh.start(t)
	exe := installed(t, "ai-usage")
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res, err := updater(srv, exe, "v1.2.9").Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), exe) {
		t.Fatalf("Check error = %v", err)
	}
	if res.Latest != "v1.3.0" || res.Installed {
		t.Fatalf("Check = %+v", res)
	}
	if gh.downloads() != 0 {
		t.Fatal("downloaded a binary it cannot install")
	}
	if read(t, exe) != "old binary" {
		t.Fatal("binary changed")
	}
}

// Windows cannot overwrite a running executable, so it is moved to .old and
// removed by a later check.
func TestCheckWindowsMovesRunningBinaryAside(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	assets := map[string][]byte{"ai-usage_windows_amd64.exe": bin}
	assets["checksums.txt"] = []byte(sumsFor(assets))
	gh := &fakeGitHub{tag: "v1.3.0", assets: assets}
	srv := gh.start(t)
	exe := installed(t, "ai-usage.exe")
	// A leftover from an older update does not block this one.
	if err := os.WriteFile(exe+".old", []byte("older binary"), 0o700); err != nil {
		t.Fatal(err)
	}

	u := updater(srv, exe, "v1.2.9")
	u.GOOS = "windows"
	res, err := u.Check(context.Background())
	if err != nil || !res.Installed {
		t.Fatalf("Check = %+v, %v", res, err)
	}
	if read(t, exe) != string(bin) || read(t, exe+".old") != "old binary" {
		t.Fatalf("binary replaced %v, old = %q", read(t, exe) == string(bin), read(t, exe+".old"))
	}
	onlyFiles(t, filepath.Dir(exe), "ai-usage.exe", "ai-usage.exe.old")

	// The next run is the new binary; its check removes the old one.
	next := updater(srv, exe, "v1.3.0")
	next.GOOS = "windows"
	if res, err := next.Check(context.Background()); err != nil || res.Installed {
		t.Fatalf("second Check = %+v, %v", res, err)
	}
	onlyFiles(t, filepath.Dir(exe), "ai-usage.exe")
}

// An old binary that still runs, as a long `schedule run` does, cannot be
// removed; it is moved aside again, and removed once it can be.
func TestCheckWindowsMovesAStillRunningOldBinaryAgain(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	assets := map[string][]byte{"ai-usage_windows_amd64.exe": bin}
	assets["checksums.txt"] = []byte(sumsFor(assets))
	srv := (&fakeGitHub{tag: "v1.3.0", assets: assets}).start(t)
	exe := installed(t, "ai-usage.exe")
	// A folder that is not empty stands in for a binary that is running:
	// os.Remove fails on both.
	running := filepath.Join(exe+".old", "image")
	if err := os.MkdirAll(running, 0o700); err != nil {
		t.Fatal(err)
	}

	u := updater(srv, exe, "v1.2.9")
	u.GOOS = "windows"
	if res, err := u.Check(context.Background()); err != nil || !res.Installed {
		t.Fatalf("Check = %+v, %v", res, err)
	}
	if read(t, exe) != string(bin) || read(t, exe+".old") != "old binary" {
		t.Fatalf("binary replaced %v, old = %q", read(t, exe) == string(bin), read(t, exe+".old"))
	}
	aside, _ := filepath.Glob(exe + ".old-*")
	if len(aside) != 1 {
		t.Fatalf("moved aside: %q", aside)
	}

	// Once it has stopped, the next check removes both.
	if err := os.Remove(filepath.Join(aside[0], "image")); err != nil {
		t.Fatal(err)
	}
	next := updater(srv, exe, "v1.3.0")
	next.GOOS = "windows"
	if _, err := next.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFiles(t, filepath.Dir(exe), "ai-usage.exe")
}

// A slow link finishes a download that takes longer than the stall limit, as
// long as bytes keep arriving.
func TestCheckSlowDownload(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	gh := newRelease(bin)
	gh.pace = 60 * time.Millisecond
	srv := gh.start(t)
	exe := installed(t, "ai-usage")
	u := updater(srv, exe, "v1.2.9")
	u.Stall = 250 * time.Millisecond
	start := time.Now()
	res, err := u.Check(context.Background())
	if err != nil || !res.Installed {
		t.Fatalf("Check = %+v, %v", res, err)
	}
	// Eight pieces 60ms apart take longer than one stall period in total, so
	// only a per-piece stall timer lets this download finish.
	if took := time.Since(start); took <= u.Stall {
		t.Fatalf("download took %s, too fast to show anything", took)
	}
	if read(t, exe) != string(bin) {
		t.Fatal("binary was not replaced")
	}
}

// The default client has no limit on the whole request, which would cut off
// a slow download, but it does not wait forever for a response either.
func TestDefaultClient(t *testing.T) {
	u := &Updater{Current: "v1.2.3", Exe: installed(t, "ai-usage")}
	if err := u.fill(); err != nil {
		t.Fatal(err)
	}
	tr, ok := u.HTTP.Transport.(*http.Transport)
	if u.HTTP.Timeout != 0 || !ok || tr.ResponseHeaderTimeout <= 0 || tr.Proxy == nil || u.Stall <= 0 || u.Lookup <= 0 {
		t.Fatalf("client timeout %s, transport %T, stall %s, lookup %s", u.HTTP.Timeout, u.HTTP.Transport, u.Stall, u.Lookup)
	}
}

func TestDevBuildsNeverUpdate(t *testing.T) {
	for _, v := range []string{"dev", "v1.2.3-dirty", "1.2"} {
		gh := newRelease([]byte("new binary"))
		srv := gh.start(t)
		exe := installed(t, "ai-usage")
		if _, err := updater(srv, exe, v).Check(context.Background()); err == nil {
			t.Fatalf("Check(%q) did not refuse", v)
		}
		if len(gh.hits) != 0 {
			t.Fatalf("Check(%q) called GitHub", v)
		}
		if !Dev(v) {
			t.Fatalf("Dev(%q) = false", v)
		}
	}
	if Dev("v1.2.3") || Dev("1.2.3") {
		t.Fatal("a release version counts as a development build")
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		tag, cur string
		want     bool
	}{
		{"v1.2.4", "v1.2.3", true},
		{"v1.3.0", "v1.2.9", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.10.0", "v1.9.0", true}, // numeric, not string, order
		{"1.2.4", "v1.2.3", true},
		{" v1.2.4\n", "v1.2.3", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.2", "v1.2.3", false},
		{"v1.2.4-rc.1", "v1.2.3", false},
		{"v1.2.4", "dev", false},
		{"latest", "v1.2.3", false},
		{"v1.2", "v1.1.0", false},
		{"v1.x.3", "v1.1.0", false},
	} {
		if got := Newer(tc.tag, tc.cur); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.tag, tc.cur, got, tc.want)
		}
	}
}

func TestChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := strings.Join([]string{
		"# not a checksum line",
		"deadbeef  ai-usage_linux_amd64", // too short
		strings.Repeat("cd", 32) + "  ai-usage_linux_amd64.sig",
		strings.ToUpper(sum) + " *ai-usage_linux_amd64", // sha256sum binary mode
		"",
	}, "\n")
	got, ok := checksum([]byte(sums), "ai-usage_linux_amd64")
	if !ok || got != sum {
		t.Fatalf("checksum = %q, %v", got, ok)
	}
	if _, ok := checksum([]byte(sums), "ai-usage_darwin_arm64"); ok {
		t.Fatal("found a checksum for an absent file")
	}
}

// A binary under a folder whose name has brackets still has its old copies
// removed.
func TestCleanupOldUnderBrackets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "John [Work]")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "ai-usage.exe")
	for _, name := range []string{"ai-usage.exe", "ai-usage.exe.old", "ai-usage.exe.old-123", "ai-usage.exe.old-456", "other.old-1"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cleanupOld(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{"ai-usage.exe", "other.old-1"}; !reflect.DeepEqual(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

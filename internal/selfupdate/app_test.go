package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// infoPlist is an Info.plist as build.sh writes it, for version v.
func infoPlist(v string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>AIUsageBar</string>
	<key>CFBundleShortVersionString</key>
	<string>` + v + `</string>
	<key>LSUIElement</key>
	<true/>
</dict>
</plist>
`
}

type zipEntry struct {
	name string
	mode fs.FileMode
	body string
}

// appEntries is the menu bar app of version v as ditto zips it: the bundle
// at the top, with a folder entry for each folder.
func appEntries(v string) []zipEntry {
	return []zipEntry{
		{AppBundle + "/", fs.ModeDir | 0o755, ""},
		{AppBundle + "/Contents/", fs.ModeDir | 0o755, ""},
		{AppBundle + "/Contents/Info.plist", 0o644, infoPlist(v)},
		{AppBundle + "/Contents/MacOS/", fs.ModeDir | 0o755, ""},
		{AppBundle + "/Contents/MacOS/AIUsageBar", 0o755, "app " + v},
	}
}

func zipOf(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// installedApp writes the menu bar app of version v into a folder of its own.
func installedApp(t *testing.T, v string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), AppBundle)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(infoPlist(v)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "AIUsageBar"), []byte("app "+v), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

// appIs fails unless the app at bundle is the one of version v.
func appIs(t *testing.T, bundle, v string) {
	t.Helper()
	if got, ok := bundleVersion(bundle); !ok || got != v {
		t.Fatalf("app version = %q, %v; want %q", got, ok, v)
	}
	if got := read(t, filepath.Join(bundle, "Contents", "MacOS", "AIUsageBar")); got != "app "+v {
		t.Fatalf("app executable = %q, want the one of %s", got, v)
	}
}

// appRelease is a v1.3.0 release with a macOS binary, the menu bar app, and
// their checksums.
func appRelease(t *testing.T, bin []byte) *fakeGitHub {
	t.Helper()
	assets := map[string][]byte{"ai-usage_darwin_amd64": bin, AppAsset: zipOf(t, appEntries("1.3.0"))}
	assets["checksums.txt"] = []byte(sumsFor(assets))
	return &fakeGitHub{tag: "v1.3.0", assets: assets}
}

// macUpdater updates exe and the app on macOS, whatever this machine is.
func macUpdater(srv *httptest.Server, exe, current, app string) *Updater {
	u := updater(srv, exe, current)
	u.GOOS, u.App = "darwin", app
	return u
}

// fetched is how many times the fake sent the file name.
func (f *fakeGitHub) fetched(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits["/storage/"+name]
}

// The app follows the binary to the latest release: when the binary is at it
// already, when this check installs it, and when a scheduled run did.
func TestCheckUpdatesApp(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	for _, tc := range []struct {
		name    string
		current string
		// inPlace says a scheduled run already put the release in place.
		inPlace bool
		want    Result
	}{
		{"binary at the release", "v1.3.0", false, Result{Latest: "v1.3.0", AppDownloaded: true}},
		{"binary installed now", "v1.2.9", false, Result{Latest: "v1.3.0", Installed: true, Downloaded: true, AppDownloaded: true}},
		{"binary installed by another run", "v1.2.9", true, Result{Latest: "v1.3.0", Installed: true, AppDownloaded: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := appRelease(t, bin)
			srv := gh.start(t)
			exe := installed(t, "ai-usage")
			if tc.inPlace {
				if err := os.WriteFile(exe, bin, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			app := installedApp(t, "1.2.0")
			res, err := macUpdater(srv, exe, tc.current, app).Check(context.Background())
			if err != nil || res != tc.want {
				t.Fatalf("Check = %+v, %v; want %+v", res, err, tc.want)
			}
			appIs(t, app, "1.3.0")
			if runtime.GOOS != "windows" {
				exec, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "AIUsageBar"))
				if err != nil {
					t.Fatal(err)
				}
				plist, err := os.Stat(filepath.Join(app, "Contents", "Info.plist"))
				if err != nil {
					t.Fatal(err)
				}
				if exec.Mode().Perm()&0o100 == 0 || plist.Mode().Perm()&0o111 != 0 {
					t.Fatalf("modes: executable %v, Info.plist %v", exec.Mode(), plist.Mode())
				}
			}
			onlyFiles(t, filepath.Dir(app), AppBundle)
			if gh.fetched(AppAsset) != 1 {
				t.Fatalf("app downloaded %d times", gh.fetched(AppAsset))
			}
		})
	}
}

// Check touches the app only to bring an installed release of it forward.
func TestCheckLeavesAppAlone(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	for _, tc := range []struct {
		name string
		// app is the version installed; "" for no app.
		app     string
		current string
		setup   func(*fakeGitHub, *Updater)
		wantErr error
	}{
		{"no app", "", "v1.3.0", nil, nil},
		{"same release", "1.3.0", "v1.3.0", nil, nil},
		{"later release", "1.4.0", "v1.3.0", nil, nil},
		{"built from source", "0.0.0-dev", "v1.3.0", nil, nil},
		{"not macOS", "1.2.0", "v1.3.0", func(f *fakeGitHub, u *Updater) { u.GOOS = "linux" }, nil},
		{"no app to keep", "1.2.0", "v1.3.0", func(f *fakeGitHub, u *Updater) { u.App = "" }, nil},
		{"release without the app", "1.2.0", "v1.3.0", func(f *fakeGitHub, u *Updater) {
			delete(f.assets, AppAsset)
			f.resum()
		}, nil},
		// A release whose binary did not install is not the app's either.
		{"skipped release", "1.2.0", "v1.2.9", func(f *fakeGitHub, u *Updater) { u.Skip = "v1.3.0" }, ErrSkipped},
		// Nor is one whose app did not install at an earlier check.
		{"skipped app", "1.2.0", "v1.3.0", func(f *fakeGitHub, u *Updater) { u.SkipApp = "v1.3.0" }, ErrAppSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := appRelease(t, bin)
			srv := gh.start(t)
			app := filepath.Join(t.TempDir(), AppBundle)
			if tc.app != "" {
				app = installedApp(t, tc.app)
			}
			u := macUpdater(srv, installed(t, "ai-usage"), tc.current, app)
			if tc.setup != nil {
				tc.setup(gh, u)
			}
			if _, err := u.Check(context.Background()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Check error = %v, want %v", err, tc.wantErr)
			}
			if gh.fetched(AppAsset) != 0 {
				t.Fatal("downloaded the app")
			}
			if tc.app == "" {
				onlyFiles(t, filepath.Dir(app))
				return
			}
			appIs(t, app, tc.app)
		})
	}
}

// An app update that fails leaves the app as it was and nothing beside it.
// Its error names the app, and the Result is still the binary's.
func TestCheckRefusesBadApps(t *testing.T) {
	bin := runnable(t, "v1.3.0")
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *fakeGitHub)
		// want is what the error must name after "menu bar app: ".
		want []string
		// downloaded says the app was downloaded, so that later checks
		// do not download it again for a while.
		downloaded bool
	}{
		{"checksum mismatch", func(t *testing.T, f *fakeGitHub) {
			f.assets[AppAsset] = zipOf(t, appEntries("1.3.0"))[1:]
		}, []string{AppAsset, "checksum"}, true},
		{"app missing", func(t *testing.T, f *fakeGitHub) { delete(f.assets, AppAsset) }, []string{"v1.3.0", AppAsset}, false},
		{"download fails", func(t *testing.T, f *fakeGitHub) {
			f.status = map[string]int{"/storage/" + AppAsset: http.StatusBadGateway}
		}, []string{"HTTP 502"}, false},
		{"not a zip", func(t *testing.T, f *fakeGitHub) {
			f.assets[AppAsset] = []byte("not a zip")
			f.resum()
		}, []string{AppAsset, "v1.3.0", "zip"}, true},
		// The checksum proves the file is what was uploaded, not that it is
		// the app of this release.
		{"app of another release", func(t *testing.T, f *fakeGitHub) {
			f.assets[AppAsset] = zipOf(t, appEntries("1.2.9"))
			f.resum()
		}, []string{AppAsset, `"1.2.9"`}, true},
		{"no Info.plist", func(t *testing.T, f *fakeGitHub) {
			e := appEntries("1.3.0")
			f.assets[AppAsset] = zipOf(t, append(e[:2:2], e[3:]...))
			f.resum()
		}, []string{"Info.plist"}, true},
		{"a file outside the bundle", func(t *testing.T, f *fakeGitHub) {
			f.assets[AppAsset] = zipOf(t, append(appEntries("1.3.0"), zipEntry{AppBundle + "/../../evil", 0o644, "evil"}))
			f.resum()
		}, []string{"outside"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := appRelease(t, bin)
			tc.setup(t, gh)
			srv := gh.start(t)
			app := installedApp(t, "1.2.0")
			res, err := macUpdater(srv, installed(t, "ai-usage"), "v1.3.0", app).Check(context.Background())
			if !errors.Is(err, ErrApp) || !strings.HasPrefix(err.Error(), "menu bar app: ") {
				t.Fatalf("Check error = %v, want one about the menu bar app", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("Check error = %v, want it to name %s", err, w)
				}
			}
			if want := (Result{Latest: "v1.3.0", AppDownloaded: tc.downloaded}); res != want {
				t.Fatalf("Check = %+v, want %+v", res, want)
			}
			appIs(t, app, "1.2.0")
			onlyFiles(t, filepath.Dir(app), AppBundle)
			if _, err := os.Lstat(filepath.Join(filepath.Dir(app), "..", "evil")); err == nil {
				t.Fatal("the archive wrote outside its folder")
			}
		})
	}
}

// An app in a folder this user cannot write reports why and does not
// download a release it cannot install.
func TestCheckAppInReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	gh := appRelease(t, []byte("new binary"))
	srv := gh.start(t)
	app := installedApp(t, "1.2.0")
	dir := filepath.Dir(app)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res, err := macUpdater(srv, installed(t, "ai-usage"), "v1.3.0", app).Check(context.Background())
	if !errors.Is(err, ErrApp) || !strings.Contains(err.Error(), "cannot write beside "+app) || res.AppDownloaded {
		t.Fatalf("Check = %+v, %v", res, err)
	}
	if gh.fetched(AppAsset) != 0 {
		t.Fatal("downloaded an app it cannot install")
	}
	appIs(t, app, "1.2.0")
}

// What an update stopped halfway left beside the app is removed by the next
// check, whether or not it updates the app. What the installer stages there
// is its own: the installer does not take the run lock, and can be at work.
func TestCheckRemovesAppLeftovers(t *testing.T) {
	gh := appRelease(t, []byte("new binary"))
	srv := gh.start(t)
	app := installedApp(t, "1.3.0")
	dir := filepath.Dir(app)
	for _, p := range []string{".ai-usage-app-123/old/Contents/Info.plist", ".ai-usage-app-456/" + AppBundle + "/x", "Other.app/Contents/Info.plist", ".ai-usage-install-789/Contents/Info.plist"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := macUpdater(srv, installed(t, "ai-usage"), "v1.3.0", app).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFiles(t, dir, ".ai-usage-install-789", AppBundle, "Other.app")
}

// A swap that cannot put the new bundle in place puts the old one back.
func TestSwapAppRestoresOldBundle(t *testing.T) {
	app := installedApp(t, "1.2.0")
	stage := t.TempDir()
	if err := swapApp(app, filepath.Join(stage, "missing"), filepath.Join(stage, "old")); err == nil {
		t.Fatal("swapApp found a bundle that is not there")
	}
	appIs(t, app, "1.2.0")
	onlyFiles(t, stage)
}

// The archive can only put folders and regular files inside the bundle, up
// to a size and a number of files.
func TestUnzipAppRefusesWhatIsNotABundle(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "evil")
	for _, tc := range []struct {
		name  string
		extra []zipEntry
		limit int64
		// want is what the error must name.
		want string
	}{
		{"parent", []zipEntry{{"../evil", 0o644, "x"}}, 0, "outside"},
		{"parent through the bundle", []zipEntry{{AppBundle + "/../../evil", 0o644, "x"}}, 0, "outside"},
		{"absolute", []zipEntry{{filepath.ToSlash(outside), 0o644, "x"}}, 0, "outside"},
		{"backslash", []zipEntry{{AppBundle + `\..\..\evil`, 0o644, "x"}}, 0, "outside"},
		{"beside the bundle", []zipEntry{{"Other.app/Contents/Info.plist", 0o644, "x"}}, 0, "outside"},
		{"nothing", []zipEntry{{"", 0o644, "x"}}, 0, "outside"},
		{"link", []zipEntry{{AppBundle + "/Contents/Resources", fs.ModeSymlink | 0o777, "../../.."}}, 0, "not a file or folder"},
		{"named pipe", []zipEntry{{AppBundle + "/Contents/pipe", fs.ModeNamedPipe | 0o644, ""}}, 0, "not a file or folder"},
		{"one name twice", []zipEntry{{AppBundle + "/Contents/Info.plist", 0o644, "again"}}, 0, "exists"},
		// Each file is within the limit; all of them are not.
		{"too large", []zipEntry{{AppBundle + "/Contents/big", 0o644, strings.Repeat("x", 200)}}, int64(len(infoPlist("1.3.0")) + 100), "more than expected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.name)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			limit := tc.limit
			if limit == 0 {
				limit = maxAppUnzipped
			}
			// The bad entry comes after a whole bundle.
			err := unzipApp(zipOf(t, append(appEntries("1.3.0"), tc.extra...)), dir, limit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unzipApp error = %v, want one naming %q", err, tc.want)
			}
			if _, err := os.Lstat(outside); err == nil {
				t.Fatal("the archive wrote outside its folder")
			}
		})
	}

	t.Run("too many files", func(t *testing.T) {
		entries := appEntries("1.3.0")
		for i := len(entries); i <= maxAppEntries; i++ {
			entries = append(entries, zipEntry{AppBundle + "/Contents/Resources/", fs.ModeDir | 0o755, ""})
		}
		err := unzipApp(zipOf(t, entries), t.TempDir(), maxAppUnzipped)
		if err == nil || !strings.Contains(err.Error(), "more than expected") {
			t.Fatalf("unzipApp error = %v", err)
		}
	})

	t.Run("a bundle", func(t *testing.T) {
		dir := t.TempDir()
		if err := unzipApp(zipOf(t, appEntries("1.3.0")), dir, maxAppUnzipped); err != nil {
			t.Fatal(err)
		}
		appIs(t, filepath.Join(dir, AppBundle), "1.3.0")
	})
}

// The version is the top dict's CFBundleShortVersionString. A plist that is
// not XML, as a binary one, names none.
func TestBundleVersion(t *testing.T) {
	for _, tc := range []struct {
		name, plist, want string
		ok                bool
	}{
		{"as build.sh writes it", infoPlist("1.3.0"), "1.3.0", true},
		{"spaces", strings.Replace(infoPlist("x"), "<string>x</string>", "<string>\n\t1.3.0 </string>", 1), "1.3.0", true},
		{"a nested dict first", `<plist><dict><key>Nested</key><dict><key>CFBundleShortVersionString</key><string>9.9.9</string></dict>` +
			`<key>CFBundleShortVersionString</key><string>1.3.0</string></dict></plist>`, "1.3.0", true},
		{"not a string", `<plist><dict><key>CFBundleShortVersionString</key><integer>1</integer></dict></plist>`, "", false},
		{"a value, not a key", `<plist><dict><key>Name</key><string>CFBundleShortVersionString</string><key>Other</key><string>1.3.0</string></dict></plist>`, "", false},
		{"no version", `<plist><dict><key>CFBundleExecutable</key><string>AIUsageBar</string></dict></plist>`, "", false},
		{"binary", "bplist00\xd1\x01\x02", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := filepath.Join(t.TempDir(), AppBundle)
			if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(tc.plist), 0o644); err != nil {
				t.Fatal(err)
			}
			if v, ok := bundleVersion(app); v != tc.want || ok != tc.ok {
				t.Fatalf("bundleVersion = %q, %v; want %q, %v", v, ok, tc.want, tc.ok)
			}
		})
	}
	if v, ok := bundleVersion(filepath.Join(t.TempDir(), AppBundle)); ok {
		t.Fatalf("bundleVersion of no app = %q", v)
	}
}

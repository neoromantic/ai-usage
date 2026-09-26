package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// AppAsset is the release file of the macOS menu bar app: its bundle,
// AppBundle, zipped by ditto with the bundle at the top.
const AppAsset = "ai-usage_darwin_app.zip"

// AppBundle is the menu bar app's bundle folder.
const AppBundle = "AI Usage.app"

// Limits on AppAsset: its size, and the number and total size of the files
// in it. The app is a few megabytes.
const (
	maxAppZip      = 64 << 20
	maxAppEntries  = 10000
	maxAppUnzipped = 256 << 20
)

// appStage starts the name of the folder an update of the app works in,
// beside the app. The installer, which does not take the run lock, stages
// under another name.
const appStage = ".ai-usage-app-"

var errAppTooLarge = errors.New("it unpacks to more than expected")

// updateApp puts the menu bar app of release tag in place of u.App when an
// older release of the app is there. It leaves alone a folder with no app,
// an app built from source, which reports no release, and an app at tag or
// later. A release whose checksums.txt does not list the app has none, and
// leaves it alone too. downloaded says it downloaded the app.
func (u *Updater) updateApp(ctx context.Context, repo, tag string) (downloaded bool, err error) {
	if u.App == "" || u.GOOS != "darwin" {
		return false, nil
	}
	cleanupApp(filepath.Dir(u.App))
	if v, ok := bundleVersion(u.App); !ok || !Newer(tag, v) {
		return false, nil
	}
	if tag == u.SkipApp {
		return false, ErrAppSkipped
	}
	if downloaded, err = u.installApp(ctx, repo, tag); err != nil {
		return downloaded, fmt.Errorf("%w: %w", ErrApp, err)
	}
	return downloaded, nil
}

// installApp downloads the menu bar app of release tag, checks it, unpacks
// it beside u.App, and swaps it in. downloaded says it got as far as the
// download.
func (u *Updater) installApp(ctx context.Context, repo, tag string) (downloaded bool, err error) {
	dir := filepath.Dir(u.App)
	// As with the binary: an app in a folder this user cannot write would
	// download every release and then fail to install it.
	if err := writable(dir); err != nil {
		return false, fmt.Errorf("cannot write beside %s: %w", u.App, err)
	}
	sums, err := u.download(ctx, repo, tag, "checksums.txt", 1<<20)
	if err != nil {
		return false, err
	}
	want, ok := checksum(sums, AppAsset)
	if !ok {
		return false, nil
	}
	zipped, err := u.download(ctx, repo, tag, AppAsset, maxAppZip)
	if err != nil {
		return false, err
	}
	got := sha256.Sum256(zipped)
	if hex.EncodeToString(got[:]) != want {
		return true, fmt.Errorf("%s does not match its checksum", AppAsset)
	}
	stage, err := os.MkdirTemp(dir, appStage+"*")
	if err != nil {
		return true, fmt.Errorf("cannot write beside %s: %w", u.App, err)
	}
	// The stage ends up holding the old app, or the new one when the swap
	// did not happen.
	defer os.RemoveAll(stage)
	if err := unzipApp(zipped, stage, maxAppUnzipped); err != nil {
		return true, fmt.Errorf("%s of release %s: %w", AppAsset, tag, err)
	}
	// The checksum proves the download is what was uploaded, not that it is
	// the app of this release.
	app := filepath.Join(stage, AppBundle)
	v, ok := bundleVersion(app)
	if !ok {
		return true, fmt.Errorf("%s of release %s has no %s/Contents/Info.plist with a version", AppAsset, tag, AppBundle)
	}
	if !sameRelease(v, tag) {
		return true, fmt.Errorf("%s of release %s holds version %q", AppAsset, tag, v)
	}
	return true, swapApp(u.App, app, filepath.Join(stage, "old"))
}

// swapApp puts the bundle unpacked at app in place of bundle with renames in
// one folder: the old bundle moves to old, and back when the new one cannot
// take its place. A running app keeps the files it has open; it notices the
// new version on disk and restarts into it.
func swapApp(bundle, app, old string) error {
	if err := os.Rename(bundle, old); err != nil {
		return err
	}
	if err := os.Rename(app, bundle); err != nil {
		return errors.Join(err, os.Rename(old, bundle))
	}
	return nil
}

// cleanupApp removes the stages that updates of the app left in dir when
// they were stopped halfway.
func cleanupApp(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), appStage) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// bundleVersion is the CFBundleShortVersionString in the bundle's
// Info.plist, which build.sh writes as XML. ok is false when there is no
// Info.plist, as when no app is installed, or it names no version.
func bundleVersion(bundle string) (v string, ok bool) {
	b, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return "", false
	}
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Text    string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if xml.Unmarshal(b, &plist) != nil {
		return "", false
	}
	// The top dict holds each key followed by its value.
	items := plist.Dict.Items
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local == "key" && items[i].Text == "CFBundleShortVersionString" && items[i+1].XMLName.Local == "string" {
			return strings.TrimSpace(items[i+1].Text), true
		}
	}
	return "", false
}

// unzipApp writes the files of a zip archive into dir. It takes only folders
// and regular files, all inside AppBundle, keeps whether a file is
// executable, and stops at maxAppEntries entries or limit bytes, so a broken
// or hostile archive cannot write outside dir, leave a link to follow, or
// fill the disk.
func unzipApp(zipped []byte, dir string, limit int64) error {
	r, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return err
	}
	if len(r.File) > maxAppEntries {
		return fmt.Errorf("it holds %d files and folders, more than expected", len(r.File))
	}
	for _, f := range r.File {
		// Zip names separate with /; \ is a separator only on Windows, and
		// no bundle has one.
		name := path.Clean(f.Name)
		if strings.Contains(f.Name, `\`) || !filepath.IsLocal(name) || name != AppBundle && !strings.HasPrefix(name, AppBundle+"/") {
			return fmt.Errorf("%q is outside %s", f.Name, AppBundle)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch mode := f.Mode(); {
		case mode.IsDir():
			err = os.MkdirAll(target, 0o755)
		case mode.IsRegular():
			var n int64
			n, err = unzipFile(f, target, mode, limit)
			limit -= n
		default:
			err = fmt.Errorf("%s is not a file or folder", f.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// unzipFile writes f to target, executable when f is, and stops after limit
// bytes. n is how many bytes it wrote.
func unzipFile(f *zip.File, target string, mode fs.FileMode, limit int64) (n int64, err error) {
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	src, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()
	// Two entries with one name make a broken archive, which O_EXCL refuses.
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}
	n, err = io.Copy(dst, io.LimitReader(src, limit+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errAppTooLarge
	}
	return n, err
}

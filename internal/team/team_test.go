package team

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// The fixed key's seed is bytes 0..31. Its fingerprint was checked against an
// independent Ed25519 + SHA-256 + base32 computation; if it changes, every
// existing team gets a new name on the relay.
const (
	fixedExport = "aiu-team-1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	fixedFP     = "kzdvvj2umnduyauf35o36k6kw462mujv"
	// Sealed with the fixed key by an earlier build. Labels already on the
	// relay must keep opening after an upgrade.
	fixedSealed = "GLt50N33eLCUDmIJHN1pXsGdIEoR4rgTw6tdz58tDaeEfptp1myz9JVjQouyCP1llLI"
	fixedPlain  = "/Users/me/src/ai-usage"
)

func mustGenerate(t *testing.T) *Key {
	t.Helper()
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestFixedKeyIsStable(t *testing.T) {
	k, err := Import(fixedExport)
	if err != nil {
		t.Fatal(err)
	}
	if k.Fingerprint() != fixedFP {
		t.Errorf("fingerprint = %s, want %s", k.Fingerprint(), fixedFP)
	}
	if k.Export() != fixedExport {
		t.Errorf("Export = %s", k.Export())
	}
	got, err := k.Open(fixedSealed)
	if err != nil || got != fixedPlain {
		t.Errorf("Open(fixed) = %q, %v; want %q", got, err, fixedPlain)
	}
}

func TestGenerateExportImport(t *testing.T) {
	k := mustGenerate(t)
	line := k.Export()
	if !strings.HasPrefix(line, exportPrefix) {
		t.Fatalf("Export = %q, want prefix %q", line, exportPrefix)
	}
	for _, in := range []string{line, line + "\n", "  " + line + "\r\n"} {
		back, err := Import(in)
		if err != nil {
			t.Fatalf("Import(%q): %v", in, err)
		}
		if back.Fingerprint() != k.Fingerprint() || !back.Public().Equal(k.Public()) || back.Export() != line {
			t.Fatal("Import did not give back the same key")
		}
		// Both copies must seal for each other: that is what joining a team means.
		if got, err := back.Open(k.Seal("hello")); err != nil || got != "hello" {
			t.Fatalf("imported key cannot open: %q, %v", got, err)
		}
	}
	if other := mustGenerate(t); other.Fingerprint() == k.Fingerprint() {
		t.Fatal("two generated keys share a fingerprint")
	}
}

// Windows PowerShell set to UTF-8 puts a byte order mark in front of text
// piped to `team join`, and an editor may save one in team.key.
func TestImportIgnoresByteOrderMark(t *testing.T) {
	k := mustGenerate(t)
	line := k.Export()
	for _, in := range []string{"\ufeff" + line + "\r\n", "\ufeff " + line + "\n", "\u200b" + line + "\u200b"} {
		back, err := Import(in)
		if err != nil || back.Fingerprint() != k.Fingerprint() {
			t.Fatalf("Import(%q): %v", in, err)
		}
	}
	path := filepath.Join(t.TempDir(), "team.key")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf"+line+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if back, err := Load(path); err != nil || back.Fingerprint() != k.Fingerprint() {
		t.Fatalf("Load with a byte order mark: %v", err)
	}
}

func TestImportRejects(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	cases := map[string]string{
		"empty":             "",
		"no prefix":         b64(seed),
		"other version":     "aiu-team-2:" + b64(seed),
		"prefix only":       exportPrefix,
		"upper prefix":      strings.ToUpper(exportPrefix) + b64(seed),
		"not base64":        exportPrefix + "not*base64!",
		"std base64 chars":  exportPrefix + strings.Repeat("+/", 21) + "A",
		"padded":            exportPrefix + base64.URLEncoding.EncodeToString(seed),
		"short seed":        exportPrefix + b64(seed[:31]),
		"long seed":         exportPrefix + b64(append(bytes.Clone(seed), 0)),
		"full private key":  exportPrefix + b64(ed25519.NewKeyFromSeed(seed)),
		"space inside":      exportPrefix + " " + b64(seed),
		"two keys pasted":   exportPrefix + b64(seed) + exportPrefix + b64(seed),
		"public key base32": fixedFP,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if k, err := Import(in); err == nil {
				t.Fatalf("Import accepted it: %s", k.Fingerprint())
			}
		})
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "team.key")
	k := mustGenerate(t)
	if err := k.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint() != k.Fingerprint() {
		t.Fatal("Load gave another key")
	}
	body, _ := os.ReadFile(path)
	if string(body) != k.Export()+"\n" {
		t.Fatalf("key file = %q", body)
	}

	// Saving over an existing key replaces it and leaves no temp files.
	k2 := mustGenerate(t)
	if err := k2.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(path); got == nil || got.Fingerprint() != k2.Fingerprint() {
		t.Fatal("second Save did not replace the key")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the key", len(entries))
	}

	if _, err := Load(filepath.Join(dir, "missing.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) err = %v, want os.ErrNotExist", err)
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(garbage) err = %v, want a parse error", err)
	}
}

func TestSaveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "team.key")
	// A leftover temp file from an older build must not lend its mode to the key.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mustGenerate(t).Save(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("key file mode = %o, want 600", mode)
	}

	fresh := filepath.Join(dir, "new", "team.key")
	if err := mustGenerate(t).Save(fresh); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Dir(fresh))
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("new key directory mode = %o, want no group or other access", mode)
	}
}

func TestFingerprintShape(t *testing.T) {
	for range 50 {
		k := mustGenerate(t)
		fp := k.Fingerprint()
		if !snapshot.ValidTeam(fp) {
			t.Fatalf("fingerprint %q is not a snapshot team", fp)
		}
		if fp != Fingerprint(k.Public()) {
			t.Fatal("Key.Fingerprint and Fingerprint(Public) differ")
		}
	}
}

func TestSealOpen(t *testing.T) {
	k := mustGenerate(t)
	for _, plain := range []string{
		"a",
		"ann",
		"/Users/ann/Developer/orbit/ai-usage",
		"C:\\Users\\Сергей\\проект",
		"emoji 😀 and \x00 nul",
		strings.Repeat("x", 300),
	} {
		sealed := k.Seal(plain)
		if sealed == plain {
			t.Fatalf("Seal(%q) returned the plaintext", plain)
		}
		// Short strings turn up in random base64 by chance.
		if len(plain) >= 6 && strings.Contains(sealed, plain) {
			t.Fatalf("Seal(%q) leaks the plaintext", plain)
		}
		if len(sealed) != k.SealedLen(len(plain)) {
			t.Fatalf("len(Seal(%d bytes)) = %d, SealedLen = %d", len(plain), len(sealed), k.SealedLen(len(plain)))
		}
		// The relay accepts it as a sealed field.
		d := validDocWith(k, sealed)
		if err := d.Validate(d.CollectedAt); err != nil {
			t.Fatalf("snapshot rejects a sealed label of %d bytes: %v", len(plain), err)
		}
		got, err := k.Open(sealed)
		if err != nil || got != plain {
			t.Fatalf("Open(Seal(%q)) = %q, %v", plain, got, err)
		}
		if again := k.Seal(plain); again == sealed {
			t.Fatal("two seals of the same text are equal; the nonce is not random")
		}
	}
}

func TestSealEmpty(t *testing.T) {
	k := mustGenerate(t)
	if s := k.Seal(""); s != "" {
		t.Fatalf("Seal(\"\") = %q, want empty", s)
	}
	if s, err := k.Open(""); s != "" || err != nil {
		t.Fatalf("Open(\"\") = %q, %v", s, err)
	}
}

func TestOpenRejects(t *testing.T) {
	k := mustGenerate(t)
	other := mustGenerate(t)
	sealed := k.Seal("secret path")
	raw, _ := base64.RawURLEncoding.DecodeString(sealed)
	flip := func(i int) string {
		b := bytes.Clone(raw)
		b[i] ^= 1
		return base64.RawURLEncoding.EncodeToString(b)
	}
	cases := map[string]string{
		"another team's key": other.Seal("secret path"),
		"nonce changed":      flip(0),
		"ciphertext changed": flip(12),
		"tag changed":        flip(len(raw) - 1),
		"truncated":          base64.RawURLEncoding.EncodeToString(raw[:len(raw)-1]),
		"shorter than nonce": base64.RawURLEncoding.EncodeToString(raw[:5]),
		"nonce only":         base64.RawURLEncoding.EncodeToString(raw[:12]),
		"not base64":         "not base64!",
		"with padding":       sealed + "==",
		"std alphabet":       strings.NewReplacer("-", "+", "_", "/").Replace(sealed) + "+/",
		"plaintext":          "secret path",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := k.Open(s); err == nil {
				t.Fatalf("Open accepted it: %q", got)
			}
		})
	}
	// Same text, other team: the AAD binds the fingerprint too.
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("another team opened the label")
	}
}

func TestSealedLenFitsSnapshot(t *testing.T) {
	k := mustGenerate(t)
	if n := k.SealedLen(0); n != len(base64.RawURLEncoding.EncodeToString(make([]byte, 12+16))) {
		t.Fatalf("SealedLen(0) = %d", n)
	}
	// The collector clips sealed text to 300 bytes before sealing.
	if n := k.SealedLen(300); n > snapshot.MaxSealed {
		t.Fatalf("SealedLen(300) = %d, over snapshot.MaxSealed %d", n, snapshot.MaxSealed)
	}
}

func TestSignVerify(t *testing.T) {
	k := mustGenerate(t)
	other := mustGenerate(t)
	msg := []byte("ai-usage snapshot v1\n{}")
	sig := k.Sign(msg)
	if !Verify(k.Public(), msg, sig) {
		t.Fatal("signature does not verify")
	}
	tampered := bytes.Clone(sig)
	tampered[0] ^= 1
	cases := []struct {
		name     string
		pub      ed25519.PublicKey
		msg, sig []byte
	}{
		{"other message", k.Public(), []byte("ai-usage snapshot v1\n{ }"), sig},
		{"tampered signature", k.Public(), msg, tampered},
		{"other key", other.Public(), msg, sig},
		{"other key's signature", k.Public(), msg, other.Sign(msg)},
		{"short signature", k.Public(), msg, sig[:10]},
		{"no signature", k.Public(), msg, nil},
		{"short key", k.Public()[:10], msg, sig},
		{"no key", nil, msg, sig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if Verify(c.pub, c.msg, c.sig) {
				t.Fatal("Verify accepted it")
			}
		})
	}
}

// validDocWith is a minimal snapshot for k whose device label is sealed.
func validDocWith(k *Key, sealed string) snapshot.Doc {
	return snapshot.Doc{
		V:                snapshot.Version,
		Team:             k.Fingerprint(),
		Device:           "test-device",
		DeviceLabel:      sealed,
		OSUser:           k.Seal("me"),
		CollectorVersion: "dev",
		CollectedAt:      time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		Accounts:         []snapshot.Account{},
		Sources:          []snapshot.Source{},
	}
}

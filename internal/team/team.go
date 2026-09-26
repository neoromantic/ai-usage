// Package team holds the team keypair.
//
// A team is one Ed25519 keypair. Every collector in the team stores the same
// private key. The public fingerprint names the team on the relay and is not
// secret. The private key signs each published snapshot and seals the strings
// in it, so the relay can check the shape but cannot read names or paths.
package team

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// exportPrefix marks an exported private key so a pasted value is recognizable.
const exportPrefix = "aiu-team-1:"

var fpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Key is a team keypair plus the symmetric key derived from it.
type Key struct {
	priv ed25519.PrivateKey
	seal cipher.AEAD
	fp   string
}

// Generate makes a new team key.
func Generate() (*Key, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return fromSeed(seed)
}

func fromSeed(seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("team key has the wrong length")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	sk, err := hkdf.Key(sha256.New, seed, nil, "ai-usage seal v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sk)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return &Key{priv: priv, seal: aead, fp: Fingerprint(pub)}, nil
}

// Fingerprint names a team: base32 of the first 20 bytes of SHA-256(public key).
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return strings.ToLower(fpEncoding.EncodeToString(sum[:20]))
}

// Fingerprint is the public team name.
func (k *Key) Fingerprint() string { return k.fp }

// Public is the public half.
func (k *Key) Public() ed25519.PublicKey { return k.priv.Public().(ed25519.PublicKey) }

// Export is the private key as one line. Whoever holds it is in the team.
func (k *Key) Export() string {
	return exportPrefix + base64.RawURLEncoding.EncodeToString(k.priv.Seed())
}

// Import reads a line made by Export.
func Import(s string) (*Key, error) {
	s = strings.TrimFunc(s, blank)
	if !strings.HasPrefix(s, exportPrefix) {
		return nil, fmt.Errorf("team key must start with %q", exportPrefix)
	}
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, exportPrefix))
	if err != nil {
		return nil, errors.New("team key is not valid base64")
	}
	return fromSeed(seed)
}

// blank is what may surround a pasted or piped key: whitespace, and the byte
// order mark that Windows PowerShell set to UTF-8 puts in front of text piped
// to a program, or that an editor saves.
func blank(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' || r == '\u200B' }

// Load reads the key file. A missing file is os.ErrNotExist.
func Load(path string) (*Key, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Import(string(body))
}

// Save writes the key file readable only by this OS user.
func (k *Key) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp always makes a new 0600 file. A fixed temp name would reuse a
	// leftover file and keep whatever looser mode it had.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.WriteString(k.Export() + "\n")
	// The key is the only way back into the team, so it must reach the disk
	// before it replaces the previous file.
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Sign signs a message with the team private key.
func (k *Key) Sign(msg []byte) []byte { return ed25519.Sign(k.priv, msg) }

// Verify checks sig over msg against pub.
func Verify(pub ed25519.PublicKey, msg, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, msg, sig)
}

// Seal encrypts a short string for the team. The empty string stays empty.
func (k *Key) Seal(plain string) string {
	if plain == "" {
		return ""
	}
	nonce := make([]byte, k.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := k.seal.Seal(nonce, nonce, []byte(plain), []byte(k.fp))
	return base64.RawURLEncoding.EncodeToString(out)
}

// Open decrypts a string made by Seal.
func (k *Key) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	n := k.seal.NonceSize()
	if len(raw) < n {
		return "", errors.New("sealed label is too short")
	}
	plain, err := k.seal.Open(nil, raw[:n], raw[n:], []byte(k.fp))
	if err != nil {
		return "", errors.New("sealed label does not open with this team key")
	}
	return string(plain), nil
}

// Package relay is the small HTTP API that stores one snapshot per device,
// and the collector's client for it.
//
// A write is accepted only when the body is a valid snapshot, byte for byte
// what encoding/json makes of it, signed by the private key whose public
// fingerprint names the team. A read must be signed
// by the same key. Readers verify every returned snapshot again, so the relay
// cannot forge or alter a team's documents without being noticed.
//
//	PUT    /v1/teams/{team}/devices/{device}   body: snapshot JSON
//	GET    /v1/teams/{team}                    signed read
//	DELETE /v1/teams/{team}/devices/{device}   signed delete
//	GET    /v1/health
package relay

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/neoromantic/ai-usage/internal/team"
)

const (
	HeaderKey  = "X-Aiu-Key"
	HeaderSig  = "X-Aiu-Signature"
	HeaderTime = "X-Aiu-Time"
	// ClockSkew is how far a signed read or delete may be from server time.
	ClockSkew = 5 * time.Minute
)

// SnapshotMessage is what a snapshot signature covers.
func SnapshotMessage(body []byte) []byte {
	return append([]byte("ai-usage snapshot v1\n"), body...)
}

// RequestMessage is what a read or delete signature covers. It names the
// method, team, and device rather than the URL path, so a proxy that rewrites
// the path does not break signatures.
func RequestMessage(method, teamFP, device string, at time.Time) []byte {
	return []byte("ai-usage request v1\n" + method + "\n" + teamFP + "\n" + device + "\n" + strconv.FormatInt(at.Unix(), 10))
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// publicKey decodes a key header and checks that it names the team.
func publicKey(header, teamFP string) (ed25519.PublicKey, error) {
	raw, err := decode(header)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("missing or malformed public key")
	}
	pub := ed25519.PublicKey(raw)
	if team.Fingerprint(pub) != teamFP {
		return nil, errors.New("public key does not match the team fingerprint")
	}
	return pub, nil
}

// ErrStatus is an HTTP error from the relay: what a server handler fails
// with, and what the client returns when the relay answers with one.
type ErrStatus struct {
	Code int
	Msg  string
}

func (e *ErrStatus) Error() string {
	if e.Msg == "" {
		return "relay: HTTP " + strconv.Itoa(e.Code)
	}
	return "relay: " + e.Msg + " (HTTP " + strconv.Itoa(e.Code) + ")"
}

// ListResponse is the team read. Body and Sig are base64url, so the exact
// signed bytes reach the reader.
type ListResponse struct {
	Devices []ListedDevice `json:"devices"`
}

type ListedDevice struct {
	Device string `json:"device"`
	Body   string `json:"body"`
	Sig    string `json:"sig"`
}

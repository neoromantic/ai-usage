package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

func TestPutRejects(t *testing.T) {
	e := newRelay(t)
	k, other := newKey(t), newKey(t)
	fp, dev := k.Fingerprint(), "work-laptop"
	body := marshal(t, docFor(k, dev, t0))
	pub := encode(k.Public())
	sig := func(key *team.Key, b []byte) string { return encode(key.Sign(SnapshotMessage(b))) }

	type req struct {
		team, device string
		key          string
		body         []byte
		sig          string
	}
	signed := func(b []byte) req { return req{fp, dev, pub, b, sig(k, b)} }
	withDoc := func(edit func(*snapshot.Doc)) []byte {
		d := docFor(k, dev, t0)
		edit(&d)
		return marshal(t, d)
	}
	edited := func(edit func(*snapshot.Doc)) req { return signed(withDoc(edit)) }

	str := string(body)
	// A repeated key: the first value is free text that Validate never sees.
	smuggled := []byte(`{"os_user":"free text, not sealed <b>hi</b>",` + str[1:])
	caseVariant := []byte(strings.Replace(str, `"device_label":`, `"Device_Label":`, 1))
	pretty, _ := json.MarshalIndent(docFor(k, dev, t0), "", "  ")
	unknown := []byte(strings.Replace(str, `{"v":1,`, `{"v":1,"note":"x",`, 1))
	big := append(bytes.Clone(body), bytes.Repeat([]byte(" "), snapshot.MaxBytes-len(body)+1)...)

	cases := []struct {
		name string
		req  req
		code int
	}{
		{"missing key", req{fp, dev, "", body, sig(k, body)}, http.StatusForbidden},
		{"malformed key", req{fp, dev, "not a key!", body, sig(k, body)}, http.StatusForbidden},
		{"short key", req{fp, dev, encode(k.Public()[:16]), body, sig(k, body)}, http.StatusForbidden},
		{"another team's key", req{fp, dev, encode(other.Public()), body, sig(other, body)}, http.StatusForbidden},
		{"missing signature", req{fp, dev, pub, body, ""}, http.StatusUnauthorized},
		{"signature not base64", req{fp, dev, pub, body, "***"}, http.StatusUnauthorized},
		{"signed by another key", req{fp, dev, pub, body, sig(other, body)}, http.StatusUnauthorized},
		{"signature over another body", req{fp, dev, pub, body, sig(k, withDoc(func(d *snapshot.Doc) { d.Accounts[0].Sessions = 9 }))}, http.StatusUnauthorized},
		{"request signature", req{fp, dev, pub, body, encode(k.Sign(RequestMessage("PUT", fp, dev, t0)))}, http.StatusUnauthorized},
		{"bare body signature", req{fp, dev, pub, body, encode(k.Sign(body))}, http.StatusUnauthorized},
		{"names another device", edited(func(d *snapshot.Doc) { d.Device = "other-device" }), http.StatusUnprocessableEntity},
		{"names another team", edited(func(d *snapshot.Doc) { d.Team = other.Fingerprint() }), http.StatusUnprocessableEntity},
		{"unknown field", signed(unknown), http.StatusUnprocessableEntity},
		{"repeated key", signed(smuggled), http.StatusUnprocessableEntity},
		{"case variant key", signed(caseVariant), http.StatusUnprocessableEntity},
		{"pretty printed", signed(pretty), http.StatusUnprocessableEntity},
		{"trailing newline", signed(append(bytes.Clone(body), '\n')), http.StatusUnprocessableEntity},
		{"wrong version", edited(func(d *snapshot.Doc) { d.V = 2 }), http.StatusUnprocessableEntity},
		{"plain text label", edited(func(d *snapshot.Doc) { d.Accounts[0].Label = "me@example.com" }), http.StatusUnprocessableEntity},
		{"quota_from its own provider", edited(func(d *snapshot.Doc) { d.Accounts[0].QuotaFrom = "codex" }), http.StatusUnprocessableEntity},
		{"quota_from not a provider", edited(func(d *snapshot.Doc) { d.Accounts[0].QuotaFrom = "free text" }), http.StatusUnprocessableEntity},
		{"collected in the future", edited(func(d *snapshot.Doc) { d.CollectedAt = t0.Add(11 * time.Minute) }), http.StatusUnprocessableEntity},
		{"over 64 KB", signed(big), http.StatusRequestEntityTooLarge},
		{"team path not a fingerprint", req{"not-a-team", dev, pub, body, sig(k, body)}, http.StatusNotFound},
		{"device path bad", req{fp, "UPPER_CASE", pub, body, sig(k, body)}, http.StatusNotFound},
		{"device path escaped slash", req{fp, "a%2Fbcdefgh", pub, body, sig(k, body)}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, msg := send(t, putRequest(t, e.ts.URL, c.req.team, c.req.device, c.req.body, c.req.key, c.req.sig))
			if resp.StatusCode != c.code {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, strings.TrimSpace(msg), c.code)
			}
			var e struct{ Error string }
			if json.Unmarshal([]byte(msg), &e) != nil || e.Error == "" {
				t.Fatalf("error body = %q", msg)
			}
		})
	}
	if n := len(stored(t, e, fp)); n != 0 {
		t.Fatalf("%d documents stored after rejected writes", n)
	}

	t.Run("method not allowed", func(t *testing.T) {
		req := putRequest(t, e.ts.URL, fp, dev, body, pub, sig(k, body))
		req.Method = http.MethodPost
		if resp, _ := send(t, req); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST status = %d", resp.StatusCode)
		}
	})
	t.Run("the same body is accepted", func(t *testing.T) {
		if resp, msg := send(t, putRequest(t, e.ts.URL, fp, dev, body, pub, sig(k, body))); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (%s)", resp.StatusCode, msg)
		}
	})
}

func TestStaleAndRepeatedWrites(t *testing.T) {
	e := newRelay(t)
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	dev := "work-laptop"
	cur := func() []byte { return stored(t, e, k.Fingerprint())[dev].Body }

	first := marshal(t, docFor(k, dev, t0))
	if err := c.Publish(ctx, dev, first); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(ctx, dev, first); err != nil {
		t.Fatalf("repeating the stored body: %v", err)
	}
	older := marshal(t, docFor(k, dev, t0.Add(-time.Minute)))
	if err := c.Publish(ctx, dev, older); statusOf(err) != http.StatusConflict {
		t.Fatalf("older snapshot: err = %v, want 409", err)
	}
	if !bytes.Equal(cur(), first) {
		t.Fatal("an older snapshot replaced a newer one")
	}
	sameTime := docFor(k, dev, t0)
	sameTime.Accounts[0].Sessions++
	sameBody := marshal(t, sameTime)
	if err := c.Publish(ctx, dev, sameBody); err != nil {
		t.Fatalf("same collected_at, new body: %v", err)
	}
	e.clock.Add(15 * time.Minute)
	newer := marshal(t, docFor(k, dev, e.clock.Now()))
	if err := c.Publish(ctx, dev, newer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cur(), newer) {
		t.Fatal("the newer snapshot was not stored")
	}
	// A replay of an earlier signed body cannot roll the device back.
	if err := c.Publish(ctx, dev, first); statusOf(err) != http.StatusConflict {
		t.Fatalf("replayed body: err = %v, want 409", err)
	}
}

func TestSignedRead(t *testing.T) {
	e := newRelay(t)
	k, other := newKey(t), newKey(t)
	ctx := context.Background()
	if err := e.client(k).Publish(ctx, "work-laptop", marshal(t, docFor(k, "work-laptop", t0))); err != nil {
		t.Fatal(err)
	}
	for _, skew := range []time.Duration{-ClockSkew, ClockSkew, 0} {
		c := e.client(k)
		c.Now = func() time.Time { return t0.Add(skew) }
		if devices, _, err := c.Pull(ctx); err != nil || len(devices) != 1 {
			t.Fatalf("skew %v: %d devices, %v", skew, len(devices), err)
		}
	}
	for _, skew := range []time.Duration{-ClockSkew - time.Second, ClockSkew + time.Second, -24 * time.Hour} {
		c := e.client(k)
		c.Now = func() time.Time { return t0.Add(skew) }
		if _, _, err := c.Pull(ctx); statusOf(err) != http.StatusUnauthorized {
			t.Fatalf("skew %v: err = %v, want 401", skew, err)
		}
	}

	fp := k.Fingerprint()
	path := "/v1/teams/" + fp
	cases := []struct {
		name string
		edit func(*http.Request)
		code int
	}{
		{"valid", func(*http.Request) {}, http.StatusOK},
		{"missing time", func(r *http.Request) { r.Header.Del(HeaderTime) }, http.StatusUnauthorized},
		{"time not a number", func(r *http.Request) { r.Header.Set(HeaderTime, "yesterday") }, http.StatusUnauthorized},
		{"time changed after signing", func(r *http.Request) { r.Header.Set(HeaderTime, strconv.FormatInt(t0.Unix()+1, 10)) }, http.StatusUnauthorized},
		{"huge time", func(r *http.Request) { r.Header.Set(HeaderTime, "9223372036854775807") }, http.StatusUnauthorized},
		{"missing signature", func(r *http.Request) { r.Header.Del(HeaderSig) }, http.StatusUnauthorized},
		{"signed by another key", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(other.Sign(RequestMessage(http.MethodGet, fp, "", t0))))
		}, http.StatusUnauthorized},
		{"missing key", func(r *http.Request) { r.Header.Del(HeaderKey) }, http.StatusForbidden},
		{"another team's key", func(r *http.Request) { r.Header.Set(HeaderKey, encode(other.Public())) }, http.StatusForbidden},
		{"signed for DELETE", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(RequestMessage(http.MethodDelete, fp, "", t0))))
		}, http.StatusUnauthorized},
		{"signed for a device", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(RequestMessage(http.MethodGet, fp, "work-laptop", t0))))
		}, http.StatusUnauthorized},
		{"snapshot signature", func(r *http.Request) {
			r.Header.Set(HeaderSig, encode(k.Sign(SnapshotMessage(RequestMessage(http.MethodGet, fp, "", t0)))))
		}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := signedRequest(t, e.ts.URL, http.MethodGet, path, k, http.MethodGet, fp, "", t0)
			c.edit(req)
			if resp, msg := send(t, req); resp.StatusCode != c.code {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, strings.TrimSpace(msg), c.code)
			}
		})
	}
	// HEAD is signed as the GET it stands for.
	if resp, _ := send(t, signedRequest(t, e.ts.URL, http.MethodHead, path, k, http.MethodGet, fp, "", t0)); resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD signed as GET: status = %d", resp.StatusCode)
	}
	if resp, _ := send(t, signedRequest(t, e.ts.URL, http.MethodHead, path, k, http.MethodHead, fp, "", t0)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD signed as HEAD: status = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/teams/NOT-A-TEAM", nil)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad team path: status = %d", resp.StatusCode)
	}
}

func TestDelete(t *testing.T) {
	e := newRelay(t)
	k, other := newKey(t), newKey(t)
	c := e.client(k)
	ctx := context.Background()
	fp := k.Fingerprint()
	for _, dev := range []string{"device-one", "device-two"} {
		if err := c.Publish(ctx, dev, marshal(t, docFor(k, dev, t0))); err != nil {
			t.Fatal(err)
		}
	}

	// A delete signature names its device.
	req := signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", k, http.MethodDelete, fp, "device-one", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete signed for another device: status = %d", resp.StatusCode)
	}
	// A read signature is not a delete signature.
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", k, http.MethodGet, fp, "device-two", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete with a GET signature: status = %d", resp.StatusCode)
	}
	// Deleting from a team needs that team's key.
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", other, http.MethodDelete, fp, "device-two", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete with another team's key: status = %d", resp.StatusCode)
	}
	// The team's public key, which is not secret, with another key's signature.
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/device-two", other, http.MethodDelete, fp, "device-two", t0)
	req.Header.Set(HeaderKey, encode(k.Public()))
	if resp, _ := send(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delete signed by another key: status = %d", resp.StatusCode)
	}
	req = signedRequest(t, e.ts.URL, http.MethodDelete, "/v1/teams/"+fp+"/devices/BAD", k, http.MethodDelete, fp, "BAD", t0)
	if resp, _ := send(t, req); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad device path: status = %d", resp.StatusCode)
	}

	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	devices, _, err := c.Pull(ctx)
	if err != nil || len(devices) != 1 || devices[0].Doc.Device != "device-one" {
		t.Fatalf("after delete: %d devices, %v", len(devices), err)
	}
}

func TestHealth(t *testing.T) {
	e := newRelay(t)
	resp, body := send(t, func() *http.Request { r, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/health", nil); return r }())
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
	var h struct {
		OK      bool `json:"ok"`
		Version int  `json:"snapshot_version"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil || !h.OK || h.Version != snapshot.Version {
		t.Fatalf("health = %s", body)
	}
}

func TestStoreFailuresAre503(t *testing.T) {
	k := newKey(t)
	ctx := context.Background()
	body := marshal(t, docFor(k, "work-laptop", t0))
	for _, op := range []string{"count", "get", "list", "put", "delete"} {
		t.Run(op, func(t *testing.T) {
			e := newRelay(t)
			e.srv.Store = &testStore{Store: e.mem, fail: map[string]bool{op: true}}
			c := e.client(k)
			var err error
			switch op {
			case "delete":
				err = c.Remove(ctx, "work-laptop")
			case "list":
				if err = c.Publish(ctx, "work-laptop", body); statusOf(err) == http.StatusServiceUnavailable {
					_, _, err = c.Pull(ctx)
				}
			default:
				err = c.Publish(ctx, "work-laptop", body)
			}
			if statusOf(err) != http.StatusServiceUnavailable {
				t.Fatalf("err = %v, want 503", err)
			}
		})
	}
}

// The canonical check must accept whatever json.Marshal makes of a collector
// document: zone offsets, nanoseconds, awkward floats, omitted fields.
func TestCollectorEncodingIsCanonical(t *testing.T) {
	e := newRelay(t)
	k := newKey(t)
	ctx := context.Background()
	zones := map[string]*time.Location{
		"utc":   time.UTC,
		"local": time.Local,
		"msk":   time.FixedZone("MSK", 3*3600),
		"nst":   time.FixedZone("NST", -(3*3600 + 30*60)),
	}
	for name, loc := range zones {
		dev := "device-" + name
		at := t0.Add(-time.Second + 123456789).In(loc)
		d := docFor(k, dev, at)
		d.LastSuccessAt = time.Time{}
		d.Accounts[0].Plan = ""
		d.Accounts[0].Windows = []snapshot.Window{
			{Name: "5h", Percent: 100.0 / 3},
			{Name: "7d", Percent: 1e-7, Minutes: 10080},
			{Name: "opus 7d", Percent: 999.9999999999999},
			{Name: "credits", Percent: 0},
		}
		d.Accounts = append(d.Accounts, snapshot.Account{
			Provider: "hermes", Label: k.Seal("openrouter"), Windows: []snapshot.Window{}, Projects: []snapshot.Project{},
		})
		if err := e.client(k).Publish(ctx, dev, marshal(t, d)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSlowBodyTimesOut(t *testing.T) {
	old := bodyTimeout
	bodyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyTimeout = old })
	e := newRelay(t)
	k := newKey(t)
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Promise a body and send one byte of it.
	fmt.Fprintf(conn, "PUT /v1/teams/%s/devices/work-laptop HTTP/1.1\r\nHost: relay\r\n%s: %s\r\nContent-Length: 1000\r\n\r\n{",
		k.Fingerprint(), HeaderKey, encode(k.Public()))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

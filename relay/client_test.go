package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/team"
)

func TestPublishPullRoundTrip(t *testing.T) {
	e := newRelay(t)
	k := newKey(t)
	ctx := context.Background()
	bodies := map[string][]byte{}
	for _, dev := range []string{"work-laptop", "home-desktop"} {
		body := marshal(t, docFor(k, dev, t0))
		bodies[dev] = body
		if err := e.client(k).Publish(ctx, dev, body); err != nil {
			t.Fatalf("Publish %s: %v", dev, err)
		}
	}
	// Another team's device must not show up.
	other := newKey(t)
	if err := e.client(other).Publish(ctx, "work-laptop", marshal(t, docFor(other, "work-laptop", t0))); err != nil {
		t.Fatal(err)
	}

	c := e.client(k)
	c.BaseURL += "/" // a configured trailing slash is fine
	res, err := c.Pull(ctx)
	if err != nil || res.Unverified != 0 || res.Undecodable != 0 {
		t.Fatalf("Pull = %+v, %v", res, err)
	}
	devices := res.Devices
	if len(devices) != 2 || devices[0].Doc.Device != "home-desktop" || devices[1].Doc.Device != "work-laptop" {
		t.Fatalf("Pull returned %+v, want both devices sorted", devices)
	}
	for _, d := range devices {
		if !bytes.Equal(d.Body, bodies[d.Doc.Device]) {
			t.Errorf("%s: body changed on the relay", d.Doc.Device)
		}
		if host, err := k.Open(d.Doc.DeviceLabel); err != nil || host != "host "+d.Doc.Device {
			t.Errorf("%s: device label opens to %q, %v", d.Doc.Device, host, err)
		}
	}
}

// Pull leaves out what does not verify and what it cannot read, and tells
// them apart: a document signed with the team key that does not decode is
// most likely from a newer release, not a forgery.
func TestPullCountsDocumentsItLeavesOut(t *testing.T) {
	e := newRelay(t)
	k, other := newKey(t), newKey(t)
	ctx := context.Background()
	fp := k.Fingerprint()
	good := marshal(t, docFor(k, "good-device", t0))
	if err := e.client(k).Publish(ctx, "good-device", good); err != nil {
		t.Fatal(err)
	}
	sign := func(key *team.Key, body []byte) []byte { return key.Sign(SnapshotMessage(body)) }
	altered := bytes.Replace(good, []byte(`"sessions":4`), []byte(`"sessions":5`), 1)
	foreign := marshal(t, docFor(other, "foreign-device", t0))
	foreign = bytes.Replace(foreign, []byte(other.Fingerprint()), []byte(fp), 1)
	otherTeam := marshal(t, docFor(other, "team-device", t0))
	junk := []byte(`{"hello":"world"}`)
	newer := bytes.Replace(marshal(t, docFor(k, "newer-device", t0)), []byte(`{"v":1,`), []byte(`{"v":1,"pace":[3],`), 1)
	unknown := bytes.Replace(marshal(t, docFor(k, "gemini-device", t0)), []byte(`"provider":"codex","label"`), []byte(`"provider":"gemini","label"`), 1)
	// The relay, or whoever controls its store, writes these directly.
	unverified := map[string]Record{
		"altered-body":   {Body: altered, Sig: sign(k, good)},
		"moved-device":   {Body: good, Sig: sign(k, good)},
		"other-team-key": {Body: foreign, Sig: sign(other, foreign)},
		"short-sig":      {Body: good, Sig: []byte("abc")},
		"no-sig":         {Body: good},
		"team-device":    {Body: otherTeam, Sig: sign(k, otherTeam)},
	}
	undecodable := map[string]Record{
		"signed-junk":   {Body: junk, Sig: sign(k, junk)},
		"newer-device":  {Body: newer, Sig: sign(k, newer)},
		"gemini-device": {Body: unknown, Sig: sign(k, unknown)},
	}
	for _, recs := range []map[string]Record{unverified, undecodable} {
		for dev, rec := range recs {
			if err := e.mem.Put(ctx, fp, dev, rec, time.Hour); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := e.client(k).Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Devices) != 1 || res.Devices[0].Doc.Device != "good-device" {
		t.Fatalf("Pull kept %d documents, want only good-device", len(res.Devices))
	}
	if res.Unverified != len(unverified) || res.Undecodable != len(undecodable) {
		t.Fatalf("unverified %d, undecodable %d; want %d and %d", res.Unverified, res.Undecodable, len(unverified), len(undecodable))
	}
}

// Each device has one document. A relay that lists one again, even with a
// genuine older snapshot, would have its tokens counted twice.
func TestPullKeepsOneDocumentPerDevice(t *testing.T) {
	k := newKey(t)
	older := marshal(t, docFor(k, "device-bbbb", t0.Add(-time.Hour)))
	newer := marshal(t, docFor(k, "device-bbbb", t0))
	other := marshal(t, docFor(k, "device-aaaa", t0))
	entry := func(body []byte) ListedDevice {
		var d struct{ Device string }
		_ = json.Unmarshal(body, &d)
		return ListedDevice{Device: d.Device, Body: encode(body), Sig: encode(k.Sign(SnapshotMessage(body)))}
	}
	list := ListResponse{Devices: []ListedDevice{entry(older), entry(newer), entry(other), entry(newer), entry(older)}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(list)
	}))
	defer ts.Close()
	res, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
	if err != nil || res.Unverified != 3 || res.Undecodable != 0 || len(res.Devices) != 2 {
		t.Fatalf("Pull = %+v, %v; want 2 devices and 3 unverified", res, err)
	}
	if res.Devices[0].Doc.Device != "device-aaaa" || !bytes.Equal(res.Devices[1].Body, newer) {
		t.Fatal("Pull did not keep the newest snapshot of each device")
	}
}

func TestPullRejectsAMalformedTeamRead(t *testing.T) {
	k := newKey(t)
	fp := k.Fingerprint()
	for name, body := range map[string]string{
		"not json":       "<html>",
		"bad base64":     `{"devices":[{"device":"good-device","body":"!!","sig":"!!"}]}`,
		"empty response": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/teams/"+fp {
					t.Errorf("path = %s", r.URL.Path)
				}
				io.WriteString(w, body)
			}))
			defer ts.Close()
			res, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
			switch name {
			case "not json":
				if err == nil {
					t.Fatal("Pull accepted HTML")
				}
			case "bad base64":
				if err != nil || res.Unverified != 1 || res.Undecodable != 0 || len(res.Devices) != 0 {
					t.Fatalf("Pull = %+v, %v", res, err)
				}
			default:
				if err != nil || res.Unverified+res.Undecodable != 0 || len(res.Devices) != 0 {
					t.Fatalf("Pull = %+v, %v", res, err)
				}
			}
		})
	}
}

func TestClientErrors(t *testing.T) {
	k := newKey(t)
	ctx := context.Background()
	for _, base := range []string{"", "  ", "/"} {
		c := &Client{BaseURL: base, Key: k}
		if err := c.Publish(ctx, "work-laptop", []byte("{}")); !errors.Is(err, errNoRelay) {
			t.Fatalf("Publish with base %q: %v", base, err)
		}
		if _, err := c.Pull(ctx); !errors.Is(err, errNoRelay) {
			t.Fatalf("Pull with base %q: %v", base, err)
		}
		if err := c.Remove(ctx, "work-laptop"); !errors.Is(err, errNoRelay) {
			t.Fatalf("Remove with base %q: %v", base, err)
		}
	}

	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	err := (&Client{BaseURL: url, Key: k}).Publish(ctx, "work-laptop", []byte("{}"))
	if err == nil || statusOf(err) != 0 || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("closed relay: %v", err)
	}

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer ts.Close()
	err = (&Client{BaseURL: ts.URL, Key: k}).Publish(ctx, "work-laptop", []byte("{}"))
	// An HTML error page is not passed on as the message.
	if statusOf(err) != http.StatusBadGateway || strings.Contains(err.Error(), "html") {
		t.Fatalf("non-JSON error: %v", err)
	}
	if errors.Is(err, ErrRejected) {
		t.Fatalf("a 502 is a rejected snapshot: %v", err)
	}
	if got := (&ErrStatus{Code: 409, Msg: "newer"}).Error(); !strings.Contains(got, "newer") || !strings.Contains(got, "409") {
		t.Fatalf("ErrStatus = %q", got)
	}
}

// A relay that refuses the snapshot itself, as one older than the collector
// refuses fields it does not know, says so apart from other failures.
func TestPublishRejected(t *testing.T) {
	e := newRelay(t)
	k := newKey(t)
	ctx := context.Background()
	c := e.client(k)
	d := docFor(k, "work-laptop", t0)
	d.Accounts[0].Plan = "<not plain>"
	err := c.Publish(ctx, "work-laptop", marshal(t, d))
	if !errors.Is(err, ErrRejected) || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("message = %q", err)
	}
	if err := c.Publish(ctx, "work-laptop", marshal(t, docFor(k, "work-laptop", t0))); err != nil {
		t.Fatal(err)
	}
	err = c.Publish(ctx, "work-laptop", marshal(t, docFor(k, "work-laptop", t0.Add(-time.Minute))))
	if statusOf(err) != http.StatusConflict || errors.Is(err, ErrRejected) {
		t.Fatalf("older snapshot: %v", err)
	}
}

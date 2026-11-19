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
		// When the relay first stored the device.
		if !d.Since.Equal(t0) {
			t.Errorf("%s: since %v, want %v", d.Doc.Device, d.Since, t0)
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
	// The altered and unsigned documents name the device they are filed
	// under, so only the signature check can leave them out.
	signed := marshal(t, docFor(k, "altered-body", t0))
	altered := bytes.Replace(signed, []byte(`"sessions":4`), []byte(`"sessions":5`), 1)
	unsigned := marshal(t, docFor(k, "unsigned-device", t0))
	otherTeam := marshal(t, docFor(other, "team-device", t0))
	newer := bytes.Replace(marshal(t, docFor(k, "newer-device", t0)), []byte(`{"v":1,`), []byte(`{"v":1,"pace":[3],`), 1)
	// The relay, or whoever controls its store, writes these directly.
	unverified := map[string]Record{
		"altered-body":    {Body: altered, Sig: sign(k, signed)},
		"unsigned-device": {Body: unsigned},
		"moved-device":    {Body: good, Sig: sign(k, good)},
		"team-device":     {Body: otherTeam, Sig: sign(k, otherTeam)},
	}
	undecodable := map[string]Record{
		"newer-device": {Body: newer, Sig: sign(k, newer)},
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

func TestClientErrors(t *testing.T) {
	k := newKey(t)
	ctx := context.Background()
	for _, base := range []string{"", "  ", "/"} {
		if err := (&Client{BaseURL: base, Key: k}).Publish(ctx, "work-laptop", []byte("{}")); !errors.Is(err, errNoRelay) {
			t.Fatalf("Publish with base %q: %v", base, err)
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
		if r.Method == http.MethodGet {
			io.WriteString(w, "<html>sign in to the network</html>")
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, Key: k}
	err = c.Publish(ctx, "work-laptop", []byte("{}"))
	// An HTML error page is not passed on as the message.
	if statusOf(err) != http.StatusBadGateway || strings.Contains(err.Error(), "html") {
		t.Fatalf("non-JSON error: %v", err)
	}
	if _, err := c.Pull(ctx); err == nil {
		t.Fatal("Pull accepted an HTML page as the team read")
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
	if statusOf(err) != http.StatusConflict || errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("older snapshot: %v", err)
	}
}

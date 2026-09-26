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
	devices, bad, err := c.Pull(ctx)
	if err != nil || bad != 0 {
		t.Fatalf("Pull = %d bad, %v", bad, err)
	}
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

func TestPullCountsDocumentsThatDoNotVerify(t *testing.T) {
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
	junk := []byte(`{"hello":"world"}`)
	// The relay, or whoever controls its store, writes these directly.
	forged := map[string]Record{
		"altered-body":   {Body: altered, Sig: sign(k, good)},
		"moved-device":   {Body: good, Sig: sign(k, good)},
		"other-team-key": {Body: foreign, Sig: sign(other, foreign)},
		"short-sig":      {Body: good, Sig: []byte("abc")},
		"signed-junk":    {Body: junk, Sig: sign(k, junk)},
		"no-sig":         {Body: good},
	}
	for dev, rec := range forged {
		if err := e.mem.Put(ctx, fp, dev, rec, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	devices, bad, err := e.client(k).Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Doc.Device != "good-device" {
		t.Fatalf("Pull kept %d documents, want only good-device", len(devices))
	}
	if bad != len(forged) {
		t.Fatalf("bad = %d, want %d", bad, len(forged))
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
	devices, bad, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
	if err != nil || bad != 3 || len(devices) != 2 {
		t.Fatalf("Pull = %d devices, %d bad, %v; want 2 devices and 3 bad", len(devices), bad, err)
	}
	if devices[0].Doc.Device != "device-aaaa" || !bytes.Equal(devices[1].Body, newer) {
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
			devices, bad, err := (&Client{BaseURL: ts.URL, Key: k}).Pull(context.Background())
			switch name {
			case "not json":
				if err == nil {
					t.Fatal("Pull accepted HTML")
				}
			case "bad base64":
				if err != nil || bad != 1 || len(devices) != 0 {
					t.Fatalf("Pull = %d devices, %d bad, %v", len(devices), bad, err)
				}
			default:
				if err != nil || bad != 0 || len(devices) != 0 {
					t.Fatalf("Pull = %d devices, %d bad, %v", len(devices), bad, err)
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
		if _, _, err := c.Pull(ctx); !errors.Is(err, errNoRelay) {
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
	if got := (&ErrStatus{Code: 409, Msg: "newer"}).Error(); !strings.Contains(got, "newer") || !strings.Contains(got, "409") {
		t.Fatalf("ErrStatus = %q", got)
	}
}

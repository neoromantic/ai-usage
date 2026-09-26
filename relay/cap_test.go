package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func TestDeviceCap(t *testing.T) {
	e := newRelay(t)
	e.srv.Limits.DevicesPerTeam = 2
	e.srv.Limits.RecordTTL = 24 * time.Hour
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	for _, dev := range []string{"device-one", "device-two"} {
		if err := e.publish(t, c, k, dev); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.publish(t, c, k, "device-three"); statusOf(err) != http.StatusForbidden {
		t.Fatalf("third device: err = %v, want 403", err)
	}
	e.clock.Add(time.Minute)
	if err := e.publish(t, c, k, "device-one"); err != nil {
		t.Fatalf("a known device must still update: %v", err)
	}
	if err := c.Remove(ctx, "device-two"); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t, c, k, "device-three"); err != nil {
		t.Fatalf("after removing a device: %v", err)
	}
	// Expired documents free their slot.
	e.clock.Add(25 * time.Hour)
	if err := e.publish(t, c, k, "device-four"); err != nil {
		t.Fatalf("after the others expired: %v", err)
	}
}

// First writes racing each other can pass the cap; the team read still lists
// only as many devices as the cap, those that joined first.
func TestTeamReadListsNoMoreThanTheCap(t *testing.T) {
	e := newRelay(t)
	e.srv.Limits.DevicesPerTeam = 3
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	e.join(t, c, k, "device-c", "device-a", "device-b")
	e.srv.Limits.DevicesPerTeam = 2
	devices, _, err := c.Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range devices {
		got = append(got, d.Doc.Device)
	}
	slices.Sort(got)
	if want := []string{"device-a", "device-c"}; !slices.Equal(got, want) {
		t.Fatalf("listed %q, want %q", got, want)
	}
}

func TestFirstWritePastTheCapGivesWay(t *testing.T) {
	e := newRelay(t)
	e.srv.Limits.DevicesPerTeam = 2
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	e.join(t, c, k, "device-a", "device-b")
	e.srv.Store = &testStore{Store: e.mem, hideFirstList: true}
	e.clock.Add(time.Minute)
	err := e.publish(t, c, k, "device-z")
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("write past the cap: %v", err)
	}
	if rec, _ := e.mem.Get(ctx, k.Fingerprint(), "device-z"); rec != nil {
		t.Fatal("the device past the cap is still stored")
	}
	// The devices already in the team keep writing.
	e.join(t, c, k, "device-a")
}

// A first write past the cap that cannot tell whether it gave way is not
// stored, or is turned away at its next write.
func TestFirstWritePastTheCapFailsClosed(t *testing.T) {
	for _, broken := range []string{"list", "delete"} {
		t.Run(broken, func(t *testing.T) {
			e := newRelay(t)
			e.srv.Limits.DevicesPerTeam = 2
			k := newKey(t)
			c := e.client(k)
			ctx := context.Background()
			e.join(t, c, k, "device-a", "device-b")
			e.srv.Store = &testStore{Store: e.mem, hideFirstList: true, fail: map[string]bool{broken: true}}
			e.clock.Add(time.Minute)
			err := e.publish(t, c, k, "device-z")
			if statusOf(err) != http.StatusServiceUnavailable {
				t.Fatalf("write past the cap: %v", err)
			}
			e.srv.Store = e.mem
			e.clock.Add(time.Minute)
			err = e.publish(t, c, k, "device-z")
			if statusOf(err) != http.StatusForbidden {
				t.Fatalf("next write past the cap: %v", err)
			}
			if rec, _ := e.mem.Get(ctx, k.Fingerprint(), "device-z"); rec != nil {
				t.Fatal("the device past the cap is still stored")
			}
		})
	}
}

// Devices are ranked by when they joined, as each handler's clock tells it,
// so a device that was told it was stored can find itself past the cap once
// a slower first write with an earlier time lands. Its next write says so.
func TestDevicePushedPastTheCapIsTold(t *testing.T) {
	e := newRelay(t)
	e.srv.Limits.DevicesPerTeam = 2
	k := newKey(t)
	c := e.client(k)
	ctx := context.Background()
	e.join(t, c, k, "device-a")
	early := e.clock.Now().Add(30 * time.Second)
	e.join(t, c, k, "device-x")
	// device-y's first write took its time before x's and lands after it.
	y := docFor(k, "device-y", e.clock.Now())
	body := marshal(t, y)
	if err := e.mem.Put(ctx, k.Fingerprint(), "device-y", Record{Body: body, Sig: k.Sign(body), Since: early}, time.Hour); err != nil {
		t.Fatal(err)
	}
	e.clock.Add(time.Minute)
	err := e.publish(t, c, k, "device-x")
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("write from the device past the cap: %v", err)
	}
	if rec, _ := e.mem.Get(ctx, k.Fingerprint(), "device-x"); rec != nil {
		t.Fatal("the device past the cap is still stored")
	}
	e.join(t, c, k, "device-y")
}

// The team read returns every snapshot in one response, and a Vercel Function
// may return at most 4.5 MB. A full team of the largest snapshots, with the
// longest device ids, must fit.
func TestFullTeamReadFitsVercel(t *testing.T) {
	out := ListResponse{}
	for i := range DefaultLimits().DevicesPerTeam {
		out.Devices = append(out.Devices, ListedDevice{
			Device: fmt.Sprintf("%064d", i),
			Body:   encode(make([]byte, snapshot.MaxBytes)),
			Sig:    encode(make([]byte, ed25519.SignatureSize)),
		})
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(out); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 4_500_000 {
		t.Fatalf("a full team read is %d bytes, over 4.5 MB", b.Len())
	}
}

// A snapshot is kept for as long as its device has been writing, at least a
// week and at most RecordTTL, so a key made to fill the store and dropped
// leaves its snapshots for a week, not 90 days.
func TestRecordLifetimeGrowsWithTheDevice(t *testing.T) {
	e := newRelay(t)
	k := newKey(t)
	c := e.client(k)
	publish := func(dev string) {
		t.Helper()
		if err := e.publish(t, c, k, dev); err != nil {
			t.Fatal(err)
		}
	}
	has := func(dev string) bool { _, ok := stored(t, e, k.Fingerprint())[dev]; return ok }
	day := 24 * time.Hour
	publish("one-off-device")
	publish("steady-device")
	e.clock.Add(6 * day)
	publish("steady-device")
	e.clock.Add(day)
	if !has("one-off-device") {
		t.Fatal("a new device's snapshot expired within a week")
	}
	e.clock.Add(time.Second)
	if has("one-off-device") {
		t.Fatal("a device written once is kept past a week")
	}
	// Written again at 12 days and at 20: each write keeps it for as long as
	// it has existed.
	e.clock.Add(5*day - time.Second)
	publish("steady-device")
	e.clock.Add(8 * day)
	publish("steady-device")
	e.clock.Add(20 * day)
	if !has("steady-device") {
		t.Fatal("a device 20 days old expired within 20 days of its last write")
	}
	e.clock.Add(time.Second)
	if has("steady-device") {
		t.Fatal("a device 20 days old is kept past 20 days")
	}
	// The lifetime never passes RecordTTL.
	if got := e.srv.recordTTL(time.Time{}); got != DefaultLimits().RecordTTL {
		t.Fatalf("recordTTL(zero since) = %v", got)
	}
}

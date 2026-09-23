package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

// Limits keep junk teams from running away with the store. The request body
// limit is snapshot.MaxBytes and is not configurable.
type Limits struct {
	DevicesPerTeam  int
	WritesPerTeam   int64 // per Window
	RequestsPerIP   int64 // per Window
	NewTeamsPerIP   int64 // per NewWindow
	NewDevicesPerIP int64 // per NewWindow
	Window          time.Duration
	NewWindow       time.Duration
	// A snapshot is kept after its last write for as long as its device has
	// been writing, but at least MinRecordTTL and at most RecordTTL.
	RecordTTL    time.Duration
	MinRecordTTL time.Duration
}

// DefaultLimits fit a team of 100 devices sampling every 15 minutes.
//
// The team read returns every device's snapshot in one response, up to about
// 44 KB each once base64 and JSON are added to 32 KB. A Vercel Function may
// return at most 4.5 MB, so 100 devices is as many as fit without paging the
// read. A device writes 4 times an hour on schedule, so 10 writes an hour per
// device leaves room for runs started by hand. A scheduled run reads the team
// once an hour, so a full team behind one NAT makes 500 requests an hour.
//
// Every new device is a new document to keep, so one address may add one full
// team's worth a day: about 4.4 MB (3.2 MB of snapshots, the rest base64 and
// JSON). What it writes once expires within a week, so a script that only
// makes keys keeps about 31 MB. One that also writes its devices again keeps
// them, and adds 4.4 MB a day for as long as it runs; the store's own size
// limit bounds that.
func DefaultLimits() Limits {
	return Limits{
		DevicesPerTeam:  100,
		WritesPerTeam:   1000,
		RequestsPerIP:   2000,
		NewTeamsPerIP:   5,
		NewDevicesPerIP: 100,
		Window:          time.Hour,
		NewWindow:       24 * time.Hour,
		RecordTTL:       90 * 24 * time.Hour,
		MinRecordTTL:    7 * 24 * time.Hour,
	}
}

// bodyTimeout bounds how long a PUT may take to send its 32 KB. It is wall
// time, not Server.Now. A variable so tests need not wait for it.
var bodyTimeout = 30 * time.Second

// Server is the relay HTTP handler.
type Server struct {
	Store  Store
	Limits Limits
	Now    func() time.Time
	// ClientIPHeader names the header in which a reverse proxy in front of the
	// relay passes the client's address, such as X-Real-Ip. Empty, the
	// default, uses the connection's own address. See clientAddr.
	ClientIPHeader string
	mux            *http.ServeMux
	blocked        overLimit
}

// withDefaults fills each unset limit from DefaultLimits, so setting one limit
// does not leave a zero Window to divide by.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.DevicesPerTeam <= 0 {
		l.DevicesPerTeam = d.DevicesPerTeam
	}
	if l.WritesPerTeam <= 0 {
		l.WritesPerTeam = d.WritesPerTeam
	}
	if l.RequestsPerIP <= 0 {
		l.RequestsPerIP = d.RequestsPerIP
	}
	if l.NewTeamsPerIP <= 0 {
		l.NewTeamsPerIP = d.NewTeamsPerIP
	}
	if l.NewDevicesPerIP <= 0 {
		l.NewDevicesPerIP = d.NewDevicesPerIP
	}
	if l.Window < time.Second {
		l.Window = d.Window
	}
	if l.NewWindow < time.Second {
		l.NewWindow = d.NewWindow
	}
	if l.RecordTTL < time.Second {
		l.RecordTTL = d.RecordTTL
	}
	if l.MinRecordTTL < time.Second {
		l.MinRecordTTL = d.MinRecordTTL
	}
	return l
}

// NewServer wires routes. A zero field in limits takes its DefaultLimits value.
func NewServer(store Store, limits Limits) *Server {
	s := &Server{Store: store, Limits: limits.withDefaults(), Now: time.Now}
	mux := http.NewServeMux()
	// Only routed requests are counted, so a path the relay does not serve
	// costs no store command.
	handle := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.perIP(h)) }
	handle("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "snapshot_version": snapshot.Version})
	})
	handle("PUT /v1/teams/{team}/devices/{device}", s.put)
	handle("DELETE /v1/teams/{team}/devices/{device}", s.del)
	handle("GET /v1/teams/{team}", s.list)
	s.mux = mux
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

// perIP counts a request against the caller's address before h serves it.
func (s *Server) perIP(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.allow(w, r, "ip:"+s.clientKey(r, 64), s.Limits.RequestsPerIP, s.Limits.Window) {
			h(w, r)
		}
	}
}

func (s *Server) now() time.Time { return s.Now().UTC() }

// allow counts one hit against key in a fixed window and answers 429 past the
// limit. A key found over its limit is remembered until its window ends, so a
// client that keeps sending costs no more store commands.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, key string, limit int64, window time.Duration) bool {
	// Server.Limits is exported and may be set after NewServer.
	win := max(int64(window/time.Second), 1)
	now := s.now().Unix()
	key += ":" + strconv.FormatInt(now/win, 10)
	// The counter starts over when this window ends, not a full window later.
	end := (now/win + 1) * win
	if !s.blocked.has(key, now) {
		n, err := s.Store.Count(r.Context(), key, time.Duration(win)*time.Second)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "store unavailable")
			return false
		}
		if n <= limit {
			return true
		}
		s.blocked.add(key, end, now)
	}
	w.Header().Set("Retry-After", strconv.FormatInt(end-now, 10))
	fail(w, http.StatusTooManyRequests, "rate limit")
	return false
}

// overLimit holds rate keys that went over their limit, each with the Unix
// time its window ends. It is per process; on Vercel each warm instance learns
// on its own, which is enough to keep one client's flood off the store.
type overLimit struct {
	mu    sync.Mutex
	until map[string]int64
	// swept is when keys whose windows had ended were last dropped.
	swept int64
}

// maxOverLimit bounds overLimit. Past it, keys are simply counted in the store.
const maxOverLimit = 4096

// has reports whether key is over its limit. Keys whose windows have ended
// are dropped at most a minute after, at the next request, so no address is
// kept past its window for long.
func (o *overLimit) has(key string, now int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.until) > 0 && now-o.swept >= 60 {
		o.sweep(now)
	}
	return o.until[key] > now
}

// add remembers key until its window ends.
func (o *overLimit) add(key string, until, now int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.until == nil {
		o.until = map[string]int64{}
	}
	o.sweep(now)
	if len(o.until) >= maxOverLimit {
		return
	}
	o.until[key] = until
}

func (o *overLimit) sweep(now int64) {
	for k, u := range o.until {
		if u <= now {
			delete(o.until, k)
		}
	}
	o.swept = now
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	teamFP, device := r.PathValue("team"), r.PathValue("device")
	if !snapshot.ValidTeam(teamFP) || !snapshot.ValidDevice(device) {
		fail(w, http.StatusNotFound, "no such team or device")
		return
	}
	pub, err := publicKey(r.Header.Get(HeaderKey), teamFP)
	if err != nil {
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	// A body that trickles in would hold a connection open for as long as the
	// client likes. Adapters without deadlines return an error we can ignore.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyTimeout))
	body, err := io.ReadAll(io.LimitReader(r.Body, snapshot.MaxBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "could not read body")
		return
	}
	if len(body) > snapshot.MaxBytes {
		fail(w, http.StatusRequestEntityTooLarge, "snapshot is larger than 32 KB")
		return
	}
	sig, err := decode(r.Header.Get(HeaderSig))
	if err != nil || !team.Verify(pub, SnapshotMessage(body), sig) {
		fail(w, http.StatusUnauthorized, "signature does not verify")
		return
	}
	doc, err := snapshot.Decode(body)
	if err == nil {
		err = doc.Validate(s.now())
	}
	if err == nil && !canonical(doc, body) {
		err = errors.New("snapshot is not in canonical form")
	}
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if doc.Team != teamFP || doc.Device != device {
		fail(w, http.StatusUnprocessableEntity, "snapshot names another team or device")
		return
	}
	if !s.allow(w, r, "team:"+teamFP, s.Limits.WritesPerTeam, s.Limits.Window) {
		return
	}

	ctx := r.Context()
	prev, err := s.Store.Get(ctx, teamFP, device)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	since := s.now()
	if prev != nil {
		// A device that racing first writes left past the cap is out of
		// every team read, so its writes are turned away too, and it is
		// told. Its record goes, so it stops counting against the cap.
		out, err := s.pastCap(ctx, teamFP, device)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "store unavailable")
			return
		}
		if out {
			_ = s.Store.Delete(context.WithoutCancel(ctx), teamFP, device)
			fail(w, http.StatusForbidden, "team has the most devices allowed")
			return
		}
		if bytes.Equal(prev.Body, body) {
			writeJSON(w, http.StatusOK, map[string]any{"stored": true})
			return
		}
		if old, err := snapshot.Decode(prev.Body); err == nil && doc.CollectedAt.Before(old.CollectedAt) {
			fail(w, http.StatusConflict, "a newer snapshot is already stored")
			return
		}
		since = prev.Since
	} else {
		devices, err := s.Store.List(ctx, teamFP)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "store unavailable")
			return
		}
		// List then Put is not atomic, so first writes from new devices racing
		// each other can pass the cap. Each looks again once it is stored, below.
		if len(devices) >= s.Limits.DevicesPerTeam {
			fail(w, http.StatusForbidden, "team has the most devices allowed")
			return
		}
		// A new device is a new document to keep. Group IPv6 by /48, which one
		// customer or one free tunnel gets, rather than /64.
		who := s.clientKey(r, 48)
		if len(devices) == 0 && !s.allow(w, r, "newteam:"+who, s.Limits.NewTeamsPerIP, s.Limits.NewWindow) {
			return
		}
		if !s.allow(w, r, "newdevice:"+who, s.Limits.NewDevicesPerIP, s.Limits.NewWindow) {
			return
		}
	}
	rec := Record{Body: body, Sig: sig, Since: since}
	if err := s.Store.Put(ctx, teamFP, device, rec, s.recordTTL(since)); err != nil {
		fail(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	if prev == nil {
		// A new device that raced others past the cap would be left out of
		// every team read while its writes succeed. The read lists the
		// devices that joined first, so one past them gives way and is told.
		// A client that hangs up does not stop this; when the answer is not
		// known, the record goes and the device tries again later.
		ctx := context.WithoutCancel(ctx)
		out, err := s.pastCap(ctx, teamFP, device)
		switch {
		case err != nil:
			_ = s.Store.Delete(ctx, teamFP, device)
			fail(w, http.StatusServiceUnavailable, "store unavailable")
			return
		case out:
			if err := s.Store.Delete(ctx, teamFP, device); err != nil {
				fail(w, http.StatusServiceUnavailable, "store unavailable")
				return
			}
			fail(w, http.StatusForbidden, "team has the most devices allowed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stored": true})
}

// pastCap reports whether the team has more devices than the cap and device
// is not among those that joined first, which team reads list. Only a team
// over the cap is listed in full.
func (s *Server) pastCap(ctx context.Context, teamFP, device string) (bool, error) {
	n, err := s.Store.Size(ctx, teamFP)
	if err != nil || n <= s.Limits.DevicesPerTeam {
		return false, err
	}
	recs, err := s.Store.List(ctx, teamFP)
	if err != nil || len(recs) <= s.Limits.DevicesPerTeam {
		return false, err
	}
	return !slices.Contains(firstDevices(recs, s.Limits.DevicesPerTeam), device), nil
}

// firstDevices lists up to n of a team's devices, the ones that joined first.
func firstDevices(recs map[string]Record, n int) []string {
	devs := slices.Collect(maps.Keys(recs))
	slices.SortFunc(devs, func(a, b string) int {
		if c := recs[a].Since.Compare(recs[b].Since); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	if len(devs) > n {
		devs = devs[:n]
	}
	return devs
}

// recordTTL keeps a snapshot for as long as its device has been writing,
// between MinRecordTTL and RecordTTL, so a key made only to fill the store
// leaves its snapshots for days, not months. A record stored before the relay
// kept Since has a zero one and keeps the full lifetime.
func (s *Server) recordTTL(since time.Time) time.Duration {
	return min(max(s.now().Sub(since), s.Limits.MinRecordTTL), s.Limits.RecordTTL)
}

// canonical reports whether body is exactly the encoding of doc. The relay
// stores the signed bytes, not doc, and Decode matches keys case-insensitively
// and lets a repeated key overwrite an earlier one, so any other body could
// carry text that Validate never saw.
func canonical(doc snapshot.Doc, body []byte) bool {
	b, err := json.Marshal(doc)
	return err == nil && bytes.Equal(b, body)
}

// signedRequest checks a read or delete signature.
func (s *Server) signedRequest(w http.ResponseWriter, r *http.Request, teamFP, device string) bool {
	pub, err := publicKey(r.Header.Get(HeaderKey), teamFP)
	if err != nil {
		fail(w, http.StatusForbidden, err.Error())
		return false
	}
	unix, err := strconv.ParseInt(r.Header.Get(HeaderTime), 10, 64)
	if err != nil {
		fail(w, http.StatusUnauthorized, "missing request time")
		return false
	}
	at := time.Unix(unix, 0)
	if d := s.now().Sub(at); d > ClockSkew || d < -ClockSkew {
		fail(w, http.StatusUnauthorized, "request time is too far from server time")
		return false
	}
	// HEAD is a GET without the body, and is signed as one.
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	sig, err := decode(r.Header.Get(HeaderSig))
	if err != nil || !team.Verify(pub, RequestMessage(method, teamFP, device, at), sig) {
		fail(w, http.StatusUnauthorized, "signature does not verify")
		return false
	}
	return true
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	teamFP := r.PathValue("team")
	if !snapshot.ValidTeam(teamFP) {
		fail(w, http.StatusNotFound, "no such team")
		return
	}
	if !s.signedRequest(w, r, teamFP, "") {
		return
	}
	recs, err := s.Store.List(r.Context(), teamFP)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	// A write that raced past the cap may not have given way yet, and the
	// read must still fit in one response: the devices that joined first are
	// listed.
	devs := firstDevices(recs, s.Limits.DevicesPerTeam)
	out := ListResponse{Devices: []ListedDevice{}}
	for _, dev := range devs {
		rec := recs[dev]
		out.Devices = append(out.Devices, ListedDevice{Device: dev, Body: encode(rec.Body), Sig: encode(rec.Sig)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	teamFP, device := r.PathValue("team"), r.PathValue("device")
	if !snapshot.ValidTeam(teamFP) || !snapshot.ValidDevice(device) {
		fail(w, http.StatusNotFound, "no such team or device")
		return
	}
	if !s.signedRequest(w, r, teamFP, device) {
		return
	}
	if err := s.Store.Delete(r.Context(), teamFP, device); err != nil {
		fail(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
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

// clientKey is the caller's rate-limit key, with IPv6 addresses grouped by
// their first v6Bits bits.
func (s *Server) clientKey(r *http.Request, v6Bits int) string {
	return rateKey(clientAddr(r, s.ClientIPHeader), v6Bits)
}

// clientAddr is the caller's address: the connection's peer, or the last
// address in header when the operator named the header their proxy sets.
//
// No header is believed by default. Caddy, cloudflared, and many load
// balancers pass a client's own X-Real-Ip through, and nginx without
// $proxy_add_x_forwarded_for passes its X-Forwarded-For, so a guess would let
// a client choose its key and skip the per-IP and new-team limits. Even a
// named header is believed only when the connection comes from loopback or a
// private network, that is from the proxy. The last address is the one the
// nearest proxy put there; the left end of X-Forwarded-For is whatever the
// client sent. Vercel's Go bridge puts the client's address in RemoteAddr,
// without a port.
func clientAddr(r *http.Request, header string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if header == "" {
		return host
	}
	peer, err := netip.ParseAddr(host)
	// No peer address means an in-process adapter, which only a platform uses.
	if err == nil && !peer.IsLoopback() && !peer.IsPrivate() && !peer.IsLinkLocalUnicast() {
		return host
	}
	if lines := r.Header.Values(header); len(lines) > 0 {
		hops := strings.Split(lines[len(lines)-1], ",")
		if ip := strings.TrimSpace(hops[len(hops)-1]); ip != "" {
			return ip
		}
	}
	return host
}

// rateKey groups IPv6 addresses by prefix, since one host is usually handed a
// whole /64 and can rotate through it.
func rateKey(ip string, v6Bits int) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		if len(ip) > 64 {
			ip = ip[:64]
		}
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, v6Bits).Masked().String()
	}
	return a.String()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ErrStatus is an HTTP error from the relay.
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

var errNoRelay = errors.New("relay is not configured")

package relay

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
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

// DefaultLimits fit a team of 50 devices sampling every 15 minutes.
//
// The team read returns every device's snapshot in one response, up to about
// 88 KB each once base64 and JSON are added to 64 KB. A Vercel Function may
// return at most 4.5 MB, so 50 devices is as many as fit without paging the
// read. A device writes 4 times an hour on schedule, so 10 writes an hour per
// device leaves room for runs started by hand. A scheduled run reads the team
// once an hour, so a full team behind one NAT makes 250 requests an hour.
//
// Every new device is a new document to keep, so one address may add two full
// teams' worth a day: about 8.8 MB (6.4 MB of snapshots, the rest base64 and
// JSON). What it writes once expires within a week, so a script that only
// makes keys keeps about 62 MB. One that also writes its devices again keeps
// them, and adds 8.8 MB a day for as long as it runs; the store's own size
// limit bounds that.
func DefaultLimits() Limits {
	return Limits{
		DevicesPerTeam:  50,
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

// allow counts one hit against key in a fixed window and fails with 429 past
// the limit. A key found over its limit is remembered until its window ends,
// so a client that keeps sending costs no more store commands.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, key string, limit int64, window time.Duration) *ErrStatus {
	// Server.Limits is exported and may be set after NewServer.
	win := max(int64(window/time.Second), 1)
	now := s.now().Unix()
	key += ":" + strconv.FormatInt(now/win, 10)
	// The counter starts over when this window ends, not a full window later.
	end := (now/win + 1) * win
	if !s.blocked.has(key, now) {
		n, err := s.Store.Count(r.Context(), key, time.Duration(win)*time.Second)
		if err != nil {
			return errUnavailable
		}
		if n <= limit {
			return nil
		}
		s.blocked.add(key, end, now)
	}
	w.Header().Set("Retry-After", strconv.FormatInt(end-now, 10))
	return &ErrStatus{Code: http.StatusTooManyRequests, Msg: "rate limit"}
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

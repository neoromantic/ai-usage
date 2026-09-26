package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

// bodyTimeout bounds how long a PUT may take to send its 64 KB. It is wall
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

// NewServer wires routes, with DefaultLimits.
func NewServer(store Store) *Server {
	s := &Server{Store: store, Limits: DefaultLimits(), Now: time.Now, mux: http.NewServeMux()}
	s.route("GET /v1/health", func(w http.ResponseWriter, r *http.Request) *ErrStatus {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "snapshot_version": snapshot.Version})
		return nil
	})
	s.route("PUT /v1/teams/{team}/devices/{device}", s.put)
	s.route("DELETE /v1/teams/{team}/devices/{device}", s.del)
	s.route("GET /v1/teams/{team}", s.list)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

var (
	errUnavailable = &ErrStatus{Code: http.StatusServiceUnavailable, Msg: "store unavailable"}
	errTeamFull    = &ErrStatus{Code: http.StatusForbidden, Msg: "team has the most devices allowed"}
)

// route serves pattern with h, after counting the request against the
// caller's address, and answers with the failure h returns. Only routed
// requests are counted, so a path the relay does not serve costs no store
// command.
func (s *Server) route(pattern string, h func(http.ResponseWriter, *http.Request) *ErrStatus) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		e := s.allow(w, r, "ip:"+s.clientKey(r, 64), s.Limits.RequestsPerIP, s.Limits.Window)
		if e == nil {
			e = h(w, r)
		}
		if e != nil {
			fail(w, e.Code, e.Msg)
		}
	})
}

func (s *Server) now() time.Time { return s.Now().UTC() }

func devicePath(r *http.Request) (teamFP, device string, e *ErrStatus) {
	teamFP, device = r.PathValue("team"), r.PathValue("device")
	if !snapshot.ValidTeam(teamFP) || !snapshot.ValidDevice(device) {
		return "", "", &ErrStatus{Code: http.StatusNotFound, Msg: "no such team or device"}
	}
	return teamFP, device, nil
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) *ErrStatus {
	teamFP, device, e := devicePath(r)
	if e != nil {
		return e
	}
	rec, doc, e := s.readSnapshot(w, r, teamFP, device)
	if e != nil {
		return e
	}
	if e = s.allow(w, r, "team:"+teamFP, s.Limits.WritesPerTeam, s.Limits.Window); e != nil {
		return e
	}
	prev, err := s.Store.Get(r.Context(), teamFP, device)
	if err != nil {
		return errUnavailable
	}
	if prev != nil {
		e = s.putAgain(r.Context(), teamFP, device, rec, doc, prev)
	} else {
		e = s.putNew(w, r, teamFP, device, rec)
	}
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, map[string]any{"stored": true})
	return nil
}

// readSnapshot reads a PUT's body and checks that the team's key signed it,
// that it is a valid snapshot in canonical form, and that it names the team
// and device of the path.
func (s *Server) readSnapshot(w http.ResponseWriter, r *http.Request, teamFP, device string) (Record, snapshot.Doc, *ErrStatus) {
	pub, err := publicKey(r.Header.Get(HeaderKey), teamFP)
	if err != nil {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusForbidden, Msg: err.Error()}
	}
	// A body that trickles in would hold a connection open for as long as the
	// client likes. Adapters without deadlines return an error we can ignore.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyTimeout))
	body, err := io.ReadAll(io.LimitReader(r.Body, snapshot.MaxBytes+1))
	if err != nil {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusBadRequest, Msg: "could not read body"}
	}
	if len(body) > snapshot.MaxBytes {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusRequestEntityTooLarge, Msg: "snapshot is larger than 64 KB"}
	}
	sig, err := decode(r.Header.Get(HeaderSig))
	if err != nil || !team.Verify(pub, SnapshotMessage(body), sig) {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusUnauthorized, Msg: "signature does not verify"}
	}
	doc, err := snapshot.Decode(body)
	if err == nil && doc.FromFuture(s.now()) {
		err = errors.New("collected_at is in the future")
	}
	if err == nil && !canonical(doc, body) {
		err = errors.New("snapshot is not in canonical form")
	}
	if err != nil {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	if doc.Team != teamFP || doc.Device != device {
		return Record{}, snapshot.Doc{}, &ErrStatus{Code: http.StatusUnprocessableEntity, Msg: "snapshot names another team or device"}
	}
	return Record{Body: body, Sig: sig}, doc, nil
}

// putAgain stores rec for a device the team already has, prev being its
// stored record. The same body again is not written.
func (s *Server) putAgain(ctx context.Context, teamFP, device string, rec Record, doc snapshot.Doc, prev *Record) *ErrStatus {
	// A device that racing first writes left past the cap is out of every
	// team read, so its writes are turned away too, and it is told. Its
	// record goes, so it stops counting against the cap.
	out, err := s.pastCap(ctx, teamFP, device)
	if err != nil {
		return errUnavailable
	}
	if out {
		_ = s.Store.Delete(context.WithoutCancel(ctx), teamFP, device)
		return errTeamFull
	}
	if bytes.Equal(prev.Body, rec.Body) {
		return nil
	}
	if old, err := snapshot.Decode(prev.Body); err == nil && doc.CollectedAt.Before(old.CollectedAt) {
		return &ErrStatus{Code: http.StatusConflict, Msg: "a newer snapshot is already stored"}
	}
	rec.Since = prev.Since
	if err := s.Store.Put(ctx, teamFP, device, rec, s.recordTTL(rec.Since)); err != nil {
		return errUnavailable
	}
	return nil
}

// putNew stores rec as a device's first write to the team.
func (s *Server) putNew(w http.ResponseWriter, r *http.Request, teamFP, device string, rec Record) *ErrStatus {
	ctx := r.Context()
	rec.Since = s.now()
	devices, err := s.Store.List(ctx, teamFP)
	if err != nil {
		return errUnavailable
	}
	// List then Put is not atomic, so first writes from new devices racing
	// each other can pass the cap. Each looks again once it is stored, below.
	if len(devices) >= s.Limits.DevicesPerTeam {
		return errTeamFull
	}
	// A new device is a new document to keep. Group IPv6 by /48, which one
	// customer or one free tunnel gets, rather than /64.
	who := s.clientKey(r, 48)
	if len(devices) == 0 {
		if e := s.allow(w, r, "newteam:"+who, s.Limits.NewTeamsPerIP, s.Limits.NewWindow); e != nil {
			return e
		}
	}
	if e := s.allow(w, r, "newdevice:"+who, s.Limits.NewDevicesPerIP, s.Limits.NewWindow); e != nil {
		return e
	}
	if err := s.Store.Put(ctx, teamFP, device, rec, s.recordTTL(rec.Since)); err != nil {
		return errUnavailable
	}
	// A new device that raced others past the cap would be left out of every
	// team read while its writes succeed. The read lists the devices that
	// joined first, so one past them gives way and is told. A client that
	// hangs up does not stop this; when the answer is not known, the record
	// goes and the device tries again later.
	ctx = context.WithoutCancel(ctx)
	out, err := s.pastCap(ctx, teamFP, device)
	switch {
	case err != nil:
		_ = s.Store.Delete(ctx, teamFP, device)
		return errUnavailable
	case out:
		if err := s.Store.Delete(ctx, teamFP, device); err != nil {
			return errUnavailable
		}
		return errTeamFull
	}
	return nil
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
// leaves its snapshots for days, not months.
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

// verifyRequest checks a read or delete signature.
func (s *Server) verifyRequest(r *http.Request, teamFP, device string) *ErrStatus {
	pub, err := publicKey(r.Header.Get(HeaderKey), teamFP)
	if err != nil {
		return &ErrStatus{Code: http.StatusForbidden, Msg: err.Error()}
	}
	unix, err := strconv.ParseInt(r.Header.Get(HeaderTime), 10, 64)
	if err != nil {
		return &ErrStatus{Code: http.StatusUnauthorized, Msg: "missing request time"}
	}
	at := time.Unix(unix, 0)
	if d := s.now().Sub(at); d > ClockSkew || d < -ClockSkew {
		return &ErrStatus{Code: http.StatusUnauthorized, Msg: "request time is too far from server time"}
	}
	// HEAD is a GET without the body, and is signed as one.
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	sig, err := decode(r.Header.Get(HeaderSig))
	if err != nil || !team.Verify(pub, RequestMessage(method, teamFP, device, at), sig) {
		return &ErrStatus{Code: http.StatusUnauthorized, Msg: "signature does not verify"}
	}
	return nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) *ErrStatus {
	teamFP := r.PathValue("team")
	if !snapshot.ValidTeam(teamFP) {
		return &ErrStatus{Code: http.StatusNotFound, Msg: "no such team"}
	}
	if e := s.verifyRequest(r, teamFP, ""); e != nil {
		return e
	}
	recs, err := s.Store.List(r.Context(), teamFP)
	if err != nil {
		return errUnavailable
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
	return nil
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) *ErrStatus {
	teamFP, device, e := devicePath(r)
	if e != nil {
		return e
	}
	if e = s.verifyRequest(r, teamFP, device); e != nil {
		return e
	}
	if err := s.Store.Delete(r.Context(), teamFP, device); err != nil {
		return errUnavailable
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

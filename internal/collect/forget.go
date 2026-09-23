package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/state"
)

// CheckReadable reports an error for the first of p's homes that is not a
// folder that can be read, with the profiles inside it, as a run reads them.
func CheckReadable(p string, homes []string, now time.Time) error {
	_, err := sessionsIn(p, withProfiles(p, homes), now)
	return err
}

// Forget removes from the ledger the sessions that p's homes gone hold, with
// the profiles inside them, so this device no longer counts them. It is for
// homes another collector reads now, such as a container's own, which counts
// their history again. kept are the homes of p that runs still read: a
// session one of them holds too stays, or the next run would count it again
// from nothing. lock takes the run lock; the homes are read under it, after
// gone left the config, so no run adds a session from them afterwards. It
// returns how many of the sessions the ledger had.
func Forget(ctx context.Context, d state.Dir, p string, gone, kept []string, now time.Time, lock func() (func(), error)) (int, error) {
	unlock, err := lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	keys, err := sessionsIn(p, withProfiles(p, gone), now)
	if err != nil {
		return 0, err
	}
	// A session the homes that stay hold but cannot read now is counted
	// from nothing if it ever reads; that is taken over refusing to forget.
	stays := map[string]bool{}
	if len(kept) > 0 {
		for _, s := range logs.ReadHomes(p, kept, now.Add(-state.Retention)).Sessions {
			stays[state.Key(p, s.ID)] = true
		}
	}
	st, err := d.LoadState()
	if err != nil {
		return 0, err
	}
	if st.Damage != "" {
		return 0, errors.New(st.Damage)
	}
	n := 0
	for k := range keys {
		if _, ok := st.Sessions[k]; ok && !stays[k] {
			delete(st.Sessions, k)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, d.SaveState(st)
}

// withProfiles is homes, each followed by the profiles inside it.
func withProfiles(p string, homes []string) []string {
	var out []string
	for _, h := range homes {
		out = append(out, h)
		out = append(out, profileHomes(p, h)...)
	}
	return out
}

// sessionsIn is the set of ledger keys of the sessions in dirs, read as a run
// reads them.
func sessionsIn(p string, dirs []string, now time.Time) (map[string]bool, error) {
	for _, h := range dirs {
		if info, err := os.Stat(h); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("cannot read %s to tell its sessions: not a directory", h)
		}
	}
	res := logs.ReadHomes(p, dirs, now.Add(-state.Retention))
	for _, h := range dirs {
		if hr := res.Homes[h]; hr.Err != nil && !errors.Is(hr.Err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot read %s to tell its sessions: %w", h, hr.Err)
		} else if hr.Unreadable > 0 {
			return nil, fmt.Errorf("cannot read %s to tell its sessions: %d unreadable files", h, hr.Unreadable)
		}
	}
	keys := map[string]bool{}
	for _, s := range res.Sessions {
		keys[state.Key(p, s.ID)] = true
	}
	return keys, nil
}

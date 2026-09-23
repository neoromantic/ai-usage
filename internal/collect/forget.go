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

// SessionsIn lists the ledger keys of the sessions that p's homes hold, and
// the profiles inside them, as a run reads them.
func SessionsIn(p string, homes []string, now time.Time) ([]string, error) {
	var read []string
	for _, h := range homes {
		if info, err := os.Stat(h); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("cannot read %s to forget its sessions: not a directory", h)
		}
		read = append(read, h)
		read = append(read, profileHomes(p, h)...)
	}
	res := logs.ReadHomes(p, read, now.Add(-state.Retention))
	for _, h := range read {
		if err := res.Homes[h].Err; err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot read %s to forget its sessions: %w", h, err)
		}
	}
	keys := make([]string, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		keys = append(keys, state.Key(p, s.ID))
	}
	return keys, nil
}

// Forget removes sessions from the ledger, so this device no longer counts
// them. It is for homes another collector reads now, such as a container's
// own, which counts their history again. lock takes the run lock. It returns
// how many of the sessions the ledger had.
func Forget(ctx context.Context, d state.Dir, keys []string, lock func() (func(), error)) (int, error) {
	unlock, err := lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	st, err := d.LoadState()
	if err != nil {
		return 0, err
	}
	if st.Damage != "" {
		return 0, errors.New(st.Damage)
	}
	n := 0
	for _, k := range keys {
		if _, ok := st.Sessions[k]; ok {
			delete(st.Sessions, k)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, d.SaveState(st)
}

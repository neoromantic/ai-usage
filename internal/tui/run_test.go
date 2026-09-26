package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/neoromantic/ai-usage/internal/view"
)

// immediate are the messages cmd and the commands it batches give at once; a
// timer, which waits an hour in these tests, gives none.
func immediate(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if b, ok := msg.(tea.BatchMsg); ok {
			var msgs []tea.Msg
			for _, c := range b {
				msgs = append(msgs, immediate(c)...)
			}
			return msgs
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func TestRefresh(t *testing.T) {
	var loads atomic.Int32
	release := make(chan error, 1)
	m := model(t, 120, 30, Config{
		Refresh: func(ctx context.Context) error { return <-release },
		Load: func(now time.Time) (view.Report, error) {
			loads.Add(1)
			return report("collected"), nil
		},
	})
	next, cmd := m.Update(press(t, "r"))
	m = next.(Model)
	if !m.busy || !strings.Contains(screen(m)[0], `busy="⠋"`) || strings.Contains(bar(m), "refresh") {
		t.Fatalf("while collecting: header %q, bar %q", screen(m)[0], bar(m))
	}
	// A second r while one runs starts nothing.
	if _, again := m.Update(press(t, "r")); again != nil {
		t.Fatal("r started a second collection")
	}
	// The spinner turns with its own ticks only.
	m = update(t, m, spinMsg{m.gen})
	if !strings.Contains(screen(m)[0], `busy="⠙"`) {
		t.Fatalf("spinner did not turn: %q", screen(m)[0])
	}
	m = update(t, m, spinMsg{m.gen - 1})
	if m.frame != 1 {
		t.Fatalf("an old spinner turned this one: frame %d", m.frame)
	}

	release <- errors.New("claude: no answer in time\nand more")
	msgs := immediate(cmd)
	if len(msgs) != 1 {
		t.Fatalf("r gave %v", msgs)
	}
	if _, ok := msgs[0].(refreshedMsg); !ok {
		t.Fatalf("collection ended with %T", msgs[0])
	}
	next, cmd = m.Update(msgs[0])
	m = next.(Model)
	rows := screen(m)
	if m.busy || !strings.Contains(rows[0], `busy=""`) || rows[len(rows)-3] != " refresh failed: claude: no answer in time" {
		t.Fatalf("after a failed collection:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(bar(m), "r refresh") {
		t.Fatalf("r is back: %q", bar(m))
	}
	// It reloads what the collection saved.
	m = update(t, m, cmd())
	if loads.Load() != 1 || !strings.HasPrefix(screen(m)[0], "collected") {
		t.Fatalf("after the reload: %d loads, header %q", loads.Load(), screen(m)[0])
	}
	// The next collection clears the error.
	m = keys(t, m, "r")
	if m.statusText() != "" {
		t.Fatalf("status %q", m.statusText())
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 17, 38, 0, 0, time.UTC)
	var asOf []time.Time
	fail := false
	m := model(t, 120, 30, Config{
		Watch: []string{state, filepath.Join(dir, "team-cache.json")},
		Now:   func() time.Time { return now },
		Load: func(at time.Time) (view.Report, error) {
			asOf = append(asOf, at)
			if fail {
				return view.Report{}, errors.New("state.json: permission denied")
			}
			return report(fmt.Sprintf("load%d", len(asOf))), nil
		},
	})
	poll := func() Model {
		t.Helper()
		next, cmd := m.Update(pollMsg(now))
		// The next poll waits; a load answers at once.
		return update(t, next.(Model), immediate(cmd)...)
	}

	now = now.Add(10 * time.Second)
	if m = poll(); len(asOf) != 0 {
		t.Fatal("reloaded with nothing new")
	}
	// A run saved new state.
	if err := os.WriteFile(state, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m = poll(); len(asOf) != 1 || !strings.HasPrefix(screen(m)[0], "load1") {
		t.Fatalf("after a save: %d loads, header %q", len(asOf), screen(m)[0])
	}
	if m = poll(); len(asOf) != 1 {
		t.Fatal("reloaded the same save twice")
	}
	// Relative times tick every 30 seconds.
	now = now.Add(31 * time.Second)
	if m = poll(); len(asOf) != 2 || !asOf[1].Equal(now) || !strings.HasPrefix(screen(m)[0], "load2") {
		t.Fatalf("after 31s: loads as of %v", asOf)
	}
	// A reload that fails keeps the page and says why.
	fail = true
	now = now.Add(31 * time.Second)
	m = poll()
	rows := screen(m)
	if !strings.HasPrefix(rows[0], "load2") || rows[len(rows)-3] != " reload failed: state.json: permission denied" {
		t.Fatalf("failed reload:\n%s", strings.Join(rows, "\n"))
	}
	fail = false
	now = now.Add(31 * time.Second)
	if m = poll(); m.statusText() != "" {
		t.Fatalf("status after a good reload: %q", m.statusText())
	}
}

func TestJobs(t *testing.T) {
	if (&jobs{ctx: context.Background()}).stop() {
		t.Fatal("stop found a job where none started")
	}
	j := &jobs{ctx: context.Background()}
	if !j.start() {
		t.Fatal("start refused")
	}
	if !j.stop() {
		t.Fatal("stop missed the running job")
	}
	if j.start() {
		t.Fatal("a job started after stop")
	}
	j.done()
	j.wg.Wait()
}

// TestRun runs the program on a pipe: it draws on the alternate screen,
// leaves it on q, and stops a collection that still runs before returning.
func TestRun(t *testing.T) {
	stopped := make(chan struct{})
	var told atomic.Bool
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{
			Report:   report("leebook"),
			Render:   fakeRender(5, 0),
			Stopping: func() { told.Store(true) },
			Refresh: func(ctx context.Context) error {
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			},
		}, in, &out)
	}()
	if _, err := w.WriteString("r"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := w.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after q")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Run returned before the collection stopped")
	}
	if !told.Load() {
		t.Fatal("Run did not say it waits for the collection")
	}
	s := out.String()
	if !strings.Contains(s, "\x1b[?1049h") || !strings.Contains(s, "\x1b[?1049l") {
		t.Fatalf("no alternate screen in and out: %q", s)
	}
}

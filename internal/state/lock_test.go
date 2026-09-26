package state

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHoldLock is the other process of TestLockEndsWithItsProcess.
func TestHoldLock(t *testing.T) {
	dir := os.Getenv("AI_USAGE_TEST_HOLD_LOCK")
	if dir == "" {
		t.Skip("run by TestLockEndsWithItsProcess")
	}
	if _, err := Dir(dir).Lock(); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
}

// TestLockEndsWithItsProcess: another process's lock holds a run off, and a
// run that was killed leaves no lock behind.
func TestLockEndsWithItsProcess(t *testing.T) {
	d := tempDir(t)
	holder := exec.Command(os.Args[0], "-test.run=^TestHoldLock$")
	holder.Env = append(os.Environ(), "AI_USAGE_TEST_HOLD_LOCK="+string(d))
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Process.Kill()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("holder: %q, %v", line, err)
	}
	if _, err := d.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock while another process holds it = %v", err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	unlock, err := d.Lock()
	if err != nil {
		t.Fatalf("the lock of a killed run was left behind: %v", err)
	}
	unlock()
}

func TestLockWait(t *testing.T) {
	defer func(d time.Duration) { lockPoll = d }(lockPoll)
	lockPoll = 5 * time.Millisecond
	ctx := context.Background()
	d := tempDir(t)
	held, err := d.Lock()
	if err != nil {
		t.Fatal(err)
	}
	// The holder finishes while the other run waits.
	waited := 0
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		held()
		close(done)
	}()
	unlock, err := d.LockWait(ctx, 10*time.Second, func() { waited++ })
	if err != nil {
		t.Fatalf("LockWait: %v", err)
	}
	<-done
	if waited != 1 {
		t.Fatalf("waiting called %d times", waited)
	}
	// No wait at all when the lock is free, and busy once the wait runs out.
	waited = 0
	start := time.Now()
	if _, err := d.LockWait(ctx, 30*time.Millisecond, func() { waited++ }); !errors.Is(err, ErrBusy) {
		t.Fatalf("LockWait while held = %v", err)
	}
	if waited != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("waited %d times for %s", waited, time.Since(start))
	}
	// Ctrl-C ends the wait.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.LockWait(cctx, time.Hour, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("LockWait after cancel = %v", err)
	}
	unlock()
	unlock, err = d.LockWait(ctx, 0, func() { t.Fatal("waited for a free lock") })
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

// A check answers at once while `schedule run` holds its lock, and checks do
// not keep each other or `schedule run` out for long.
func TestForegroundAnswersAtOnce(t *testing.T) {
	d := Dir(t.TempDir())
	if d.Foreground() {
		t.Fatal("foreground with no schedule run")
	}
	unlock, err := d.ScheduleLock()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range 3 {
		if !d.Foreground() {
			t.Fatal("not foreground while schedule run holds the lock")
		}
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("three checks took %s", took)
	}
	unlock()
	if d.Foreground() {
		t.Fatal("foreground after schedule run stopped")
	}
	if unlock, err := d.ScheduleLock(); err != nil {
		t.Fatalf("schedule lock after checks: %v", err)
	} else {
		unlock()
	}
}

package schedule

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AIU_SCHEDULE_HELPER", "echo")
	out, err := execRunner(ctx, os.Args[0], helperArgs, []byte("MAILTO=x\n"))
	if err != nil || string(out) != "MAILTO=x\n" {
		t.Fatalf("echo = %q, %v", out, err)
	}

	t.Setenv("AIU_SCHEDULE_HELPER", "no-crontab")
	_, err = execRunner(ctx, os.Args[0], helperArgs, nil)
	// The error is the first line of stderr.
	if err == nil || !strings.Contains(err.Error(), "no crontab for tester") || strings.Contains(err.Error(), "second line") {
		t.Fatalf("stderr is not the error: %v", err)
	}
	if !noCrontab(err.Error()) {
		t.Fatal("no crontab is not recognized through the runner")
	}
}

var helperArgs = []string{"-test.run=^TestHelperProcess$"}

// silentExit is a command that fails with no output at all, as a killed or
// timed-out crontab does.
func silentExit(t *testing.T) error {
	t.Setenv("AIU_SCHEDULE_HELPER", "silent")
	out, err := execRunner(context.Background(), os.Args[0], helperArgs, nil)
	if err == nil || len(out) != 0 {
		t.Fatalf("silent helper = %q, %v", out, err)
	}
	return err
}

// TestHelperProcess stands in for crontab when the test binary runs itself.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("AIU_SCHEDULE_HELPER") {
	case "":
		return
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "no-crontab":
		fmt.Fprint(os.Stderr, "crontab: no crontab for tester\nsecond line\n")
		os.Exit(1)
	case "silent":
		os.Exit(1)
	}
	os.Exit(2)
}

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"unicode"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/view"
)

// deviceName is this device's label, the name it goes by in the team:
// AI_USAGE_NAME, else the name set with `ai-usage name set`, else the host
// name. The report calls it by its OS user at that name, as view.DeviceName
// says.
func deviceName(cfg state.Config) string {
	if v, ok := envName(); ok {
		return v
	}
	if cfg.Name != "" {
		return cfg.Name
	}
	return hostname()
}

// envName is AI_USAGE_NAME when it is set and a name `ai-usage name set`
// would take.
func envName() (string, bool) {
	v := strings.TrimSpace(os.Getenv("AI_USAGE_NAME"))
	return v, v != "" && validName(v) == nil
}

// maxName keeps a device name short enough for the report's columns.
const maxName = 64

// validName says what is wrong with a device name, if anything.
func validName(n string) error {
	switch {
	case n == "":
		return errors.New("a name cannot be empty")
	case len([]rune(n)) > maxName:
		return fmt.Errorf("a name is at most %d characters", maxName)
	case strings.ContainsFunc(n, unicode.IsControl):
		return errors.New("a name cannot contain control characters")
	case snapshot.Printable(n) != n:
		return errors.New("a name cannot contain invisible characters that change the text's direction")
	}
	return nil
}

// hostname and osUser name this device in reports. Tests pin them.
var hostname = func() string {
	// macOS takes its host name from the network, so one Mac can read as
	// Mac.localdomain on one network and by its own name on another. Its
	// local host name, set in Sharing settings, stays put.
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/sbin/scutil", "--get", "LocalHostName").Output(); err == nil {
			if h := strings.TrimSpace(string(out)); h != "" {
				return h
			}
		}
	}
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSuffix(h, ".local")
}

var osUser = func() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return os.Getenv("USERNAME")
}

func cmdName(args []string, stdout io.Writer) error {
	sub, args := subcommand(args)
	d, cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch sub {
	case "", "show":
		if len(args) != 0 {
			return usageError("name show takes no arguments")
		}
		showName(cfg, stdout)
		return nil
	case "set":
		if len(args) != 1 {
			return usageError("name set takes one name; quote a name with spaces")
		}
		n := strings.TrimSpace(args[0])
		if err := validName(n); err != nil {
			return usageError(err.Error())
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.Name = n; return nil }); err != nil {
			return err
		}
		if v, ok := envName(); ok {
			fmt.Fprintf(stdout, "saved %s; while AI_USAGE_NAME is set, this device is %s\n", n, v)
			return nil
		}
		fmt.Fprintf(stdout, "this device is now %s; the team sees %s after the next run\n", n, view.DeviceName(n, osUser()))
		return nil
	case "clear":
		if len(args) != 0 {
			return usageError("name clear takes no arguments")
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.Name = ""; return nil }); err != nil {
			return err
		}
		if v, ok := envName(); ok {
			fmt.Fprintf(stdout, "cleared; while AI_USAGE_NAME is set, this device is %s\n", v)
			return nil
		}
		fmt.Fprintf(stdout, "this device goes by its host name, %s, again%s\n", hostname(), teamSees(hostname(), "; "))
		return nil
	default:
		return usageError("unknown name command " + sub)
	}
}

func showName(cfg state.Config, stdout io.Writer) {
	v, ok := envName()
	if v != "" && !ok {
		fmt.Fprintf(stdout, "AI_USAGE_NAME is ignored: %s\n", validName(v))
	}
	switch {
	case ok:
		saved := cfg.Name
		if saved == "" {
			saved = hostname()
		}
		if saved == v {
			fmt.Fprintf(stdout, "%s (from AI_USAGE_NAME%s)\n", v, teamSees(v, "; "))
			break
		}
		// launchd and cron start runs without the shell's variables.
		fmt.Fprintf(stdout, "%s (from AI_USAGE_NAME, in runs that see it%s; runs without it, such as the system scheduler's, use %s)\n", v, teamSees(v, "; "), saved)
	case cfg.Name != "":
		if s := teamSees(cfg.Name, ""); s != "" {
			fmt.Fprintf(stdout, "%s (%s)\n", cfg.Name, s)
			break
		}
		fmt.Fprintln(stdout, cfg.Name)
	default:
		fmt.Fprintf(stdout, "%s (the host name%s; `ai-usage name set NAME` names this device)\n", hostname(), teamSees(hostname(), "; "))
	}
}

// teamSees says, after sep, what the report calls this device by the name
// n: its OS user at n. It is "" where that is n itself, as for a name with
// an @ in it.
func teamSees(n, sep string) string {
	if full := view.DeviceName(n, osUser()); full != n {
		return sep + "the team sees " + full
	}
	return ""
}

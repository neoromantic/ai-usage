package state

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// TestEditConfigKeepsConcurrentEdits: commands and runs that change the
// config at the same time each keep their change.
func TestEditConfigKeepsConcurrentEdits(t *testing.T) {
	defer func(d time.Duration) { lockPoll = d }(lockPoll)
	lockPoll = time.Millisecond
	d := tempDir(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, err := d.EditConfig(func(c *Config) error {
				if c.Homes == nil {
					c.Homes = map[string][]string{}
				}
				c.Homes["claude"] = append(c.Homes["claude"], "/h/"+strconv.Itoa(i))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	c, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Homes["claude"]) != 8 {
		t.Fatalf("homes = %q", c.Homes["claude"])
	}
}

func TestLoadConfigCreatesDeviceOnce(t *testing.T) {
	d := tempDir(t)
	c1, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ValidDevice(c1.Device) {
		t.Fatalf("device id %q is not valid", c1.Device)
	}
	c2, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c2.Device != c1.Device {
		t.Fatalf("device id changed between loads: %q then %q", c1.Device, c2.Device)
	}
	checkPrivate(t, d.Path("config.json"))
}

func TestLoadConfigReplacesInvalidDeviceAndKeepsTheRest(t *testing.T) {
	d := tempDir(t)
	if err := d.SaveConfig(Config{Device: "NOT A DEVICE", Relay: "https://relay.example", ScheduleOff: true}); err != nil {
		t.Fatal(err)
	}
	c, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ValidDevice(c.Device) || c.Relay != "https://relay.example" || !c.ScheduleOff {
		t.Fatalf("config = %+v", c)
	}
	again, _ := d.LoadConfig()
	if again.Device != c.Device {
		t.Fatal("replacement device id was not saved")
	}
}

func TestLoadConfigRejectsDamagedFile(t *testing.T) {
	d := tempDir(t)
	if err := fsutil.WriteFile(d.Path("config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LoadConfig(); err == nil {
		t.Fatal("damaged config.json loaded without error")
	}
}

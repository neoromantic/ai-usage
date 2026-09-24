package main

import "testing"

// A Windows console is asked for its background only with no key typed
// ahead, since Lip Gloss drops the console's waiting input as it asks.
func TestTypedAhead(t *testing.T) {
	const (
		enter = 0x0d
		a     = 0x41
		up    = 0x26
		shift = 0x10
		ctrl  = 0xa2 // VK_LCONTROL
		caps  = 0x14
	)
	for _, c := range []struct {
		name string
		keys []consoleKey
		want bool
	}{
		{"nothing waits", nil, false},
		{"the Enter that started it goes up", []consoleKey{{down: false, vk: enter}}, false},
		{"a letter", []consoleKey{{down: false, vk: enter}, {down: true, vk: a}, {down: false, vk: a}}, true},
		{"an arrow", []consoleKey{{down: true, vk: up}}, true},
		{"Enter again", []consoleKey{{down: true, vk: enter}}, true},
		{"a modifier alone", []consoleKey{{down: true, vk: shift}, {down: true, vk: ctrl}, {down: true, vk: caps}}, false},
		{"a capital letter", []consoleKey{{down: true, vk: shift}, {down: true, vk: a}}, true},
	} {
		if got := typedAhead(c.keys); got != c.want {
			t.Errorf("%s: typedAhead = %v, want %v", c.name, got, c.want)
		}
	}
}

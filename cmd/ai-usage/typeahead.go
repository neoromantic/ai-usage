package main

// consoleKey is a key event waiting in a Windows console's input: whether
// the key went down or up, and its virtual-key code.
type consoleKey struct {
	down bool
	vk   uint16
}

// typedAhead says whether a Windows console's waiting key events hold a key
// typed ahead: one that went down, other than a modifier or lock key alone,
// which types nothing. A key that went up types nothing either; the Enter
// that started the program leaves one.
func typedAhead(keys []consoleKey) bool {
	for _, k := range keys {
		if k.down && !modifierKey(k.vk) {
			return true
		}
	}
	return false
}

// modifierKey says a virtual-key code is Shift, Ctrl, Alt, a Windows key, or
// a lock key.
func modifierKey(vk uint16) bool {
	switch vk {
	case 0x10, 0x11, 0x12, // VK_SHIFT, VK_CONTROL, VK_MENU
		0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, // VK_LSHIFT to VK_RMENU
		0x5b, 0x5c, // VK_LWIN, VK_RWIN
		0x14, 0x90, 0x91: // VK_CAPITAL, VK_NUMLOCK, VK_SCROLL
		return true
	}
	return false
}

package snapshot

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// MaxAliasWidth is how wide an account's short name may be, in columns: a
// column of the device matrix.
const MaxAliasWidth = 12

// CheckAlias says what is wrong with an account's short name, if anything.
// The snapshot seals the name, so the relay cannot check it. The command
// that sets a name and the report that shows one both do, keeping it to a
// matrix column and to text a terminal shows as it is.
func CheckAlias(n string) error {
	switch {
	case n == "":
		return errors.New("a name cannot be empty")
	case strings.ContainsFunc(n, unicode.IsSpace):
		return errors.New("a name cannot contain spaces")
	case strings.ContainsFunc(n, unicode.IsControl):
		return errors.New("a name cannot contain control characters")
	case Printable(n) != n || strings.ContainsFunc(n, func(r rune) bool { return !unicode.IsGraphic(r) }):
		return errors.New("a name cannot contain invisible characters")
	case AliasWidth(n) > MaxAliasWidth:
		return fmt.Errorf("a name is at most %d columns wide", MaxAliasWidth)
	}
	return nil
}

// AliasWidth is how many columns n takes, counting East Asian wide runes and
// emoji as two. A combining mark counts as one, so it errs long.
func AliasWidth(n string) int {
	w := 0
	for _, r := range n {
		switch {
		case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
			r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE4F, r >= 0xFF00 && r <= 0xFF60,
			r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1FAFF, r >= 0x20000 && r <= 0x3FFFD:
			w += 2
		default:
			w++
		}
	}
	return w
}

// LabelKey keys an account's label so that one person is one key: an email
// in lower case, since the harnesses spell emails in any case, and any other
// label as it is.
func LabelKey(label string) string {
	if strings.Contains(label, "@") {
		return strings.ToLower(label)
	}
	return label
}

// AliasWins says whether a name set at at on device replaces one set at cur
// on curDevice. The newest wins, and of two set at once, the one from the
// smaller device id, so the alias command and the report pick the same name.
func AliasWins(at time.Time, device string, cur time.Time, curDevice string) bool {
	return at.After(cur) || (at.Equal(cur) && device < curDevice)
}

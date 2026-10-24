package schedule

import (
	"context"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// Entry is what the scheduler's entry for the collector runs: the binary,
// and the state folder its --home names. Both are empty when there is no
// entry, or where it is not read back.
type Entry struct {
	Program, Home string
}

// Entry reads back what the scheduler's entry runs, whichever binary and
// state folder that is: on macOS from the launch agent's plist, on Linux
// from the crontab line, active or commented out. The Windows task is not
// read back. It changes nothing.
func (s Scheduler) Entry(ctx context.Context) (Entry, error) {
	switch s.GOOS {
	case "darwin":
		file, err := s.plist()
		if err != nil {
			return Entry{}, err
		}
		b, err := os.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, nil
		}
		if err != nil {
			return Entry{}, err
		}
		return entryOf(plistArgs(b)), nil
	case "windows":
		return Entry{}, nil
	}
	lines, err := s.cronLines(ctx)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	for _, l := range lines {
		if !isOurs(l) {
			continue
		}
		got := entryOf(cronArgs(l))
		if e.Program == "" || !commented(l) {
			e = got
		}
		if !commented(l) {
			break
		}
	}
	return e, nil
}

// entryOf is the entry of a command: its binary, and the folder after its
// --home.
func entryOf(argv []string) Entry {
	if len(argv) == 0 {
		return Entry{}
	}
	e := Entry{Program: argv[0]}
	if i := slices.Index(argv, "--home"); i > 0 && i+1 < len(argv) {
		e.Home = argv[i+1]
	}
	return e
}

// plistArgs is the ProgramArguments of a launch agent's plist.
func plistArgs(b []byte) []string {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	var args []string
	key, prev, in, depth := "", "", false, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return args
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "array" && prev == "ProgramArguments" && depth == 3 {
				in = true
			}
			key = t.Name.Local
		case xml.EndElement:
			depth--
			if t.Name.Local == "array" && in {
				return args
			}
			key = ""
		case xml.CharData:
			switch {
			case in && key == "string":
				args = append(args, string(t))
			case key == "key" && depth == 3:
				prev = string(t)
			case key != "" && depth == 3:
				prev = ""
			}
		}
	}
}

// cronArgs is the command of a crontab line Line wrote, active or
// commented out: the words after the five time fields, and after the PATH
// it sets, up to the first redirection, as sh reads them.
func cronArgs(line string) []string {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
	line = strings.TrimSuffix(line, "# "+Marker)
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return nil
	}
	for range 5 {
		line = strings.TrimSpace(line)
		line = line[strings.IndexAny(line, " \t"):]
	}
	words := shWords(strings.ReplaceAll(line, `\%`, "%"))
	if len(words) > 0 && strings.HasPrefix(words[0], "PATH=") {
		words = words[1:]
	}
	if i := slices.IndexFunc(words, func(w string) bool { return strings.HasPrefix(w, ">") }); i >= 0 {
		words = words[:i]
	}
	return words
}

// shWords splits s into words as sh does for the quoting shq writes: text
// in single quotes is kept as it is, a backslash outside them keeps the
// character after it, and blanks outside them end a word.
func shWords(s string) []string {
	var words []string
	var w strings.Builder
	inWord, quoted, escaped := false, false, false
	for _, r := range s {
		switch {
		case escaped:
			w.WriteRune(r)
			escaped = false
		case quoted:
			if r == '\'' {
				quoted = false
			} else {
				w.WriteRune(r)
			}
		case r == '\'':
			quoted, inWord = true, true
		case r == '\\':
			escaped, inWord = true, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, w.String())
				w.Reset()
				inWord = false
			}
		default:
			w.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, w.String())
	}
	return words
}

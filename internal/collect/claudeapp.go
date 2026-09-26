package collect

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The Claude desktop app runs each agent-mode session with a Claude Code home
// of its own, in claudeAppSessions in its data directory, and keeps its
// record of the session beside that home: local_<id>/.claude and
// local_<id>.json. claudeAppHomes are the patterns of those homes there.
const claudeAppSessions = "local-agent-mode-sessions"

var claudeAppHomes = []string{"*/*/local_*/.claude", "*/*/agent/local_*/.claude"}

// claudeAppProject is the project of an app session the person gave no
// folder. The working directory in its log is inside the app's virtual
// machine, which says nothing about the host.
const claudeAppProject = "Claude app"

// claudeAppSessionHomes lists the homes of the Claude app's sessions, for p
// "claude". There is one per session, so they are found by path on every
// run rather than remembered.
func claudeAppSessionHomes(p, userHome string, getenv func(string) string) []string {
	if p != "claude" {
		return nil
	}
	var out []string
	for _, data := range appDataDirs("Claude", userHome, getenv) {
		root := filepath.Join(data, claudeAppSessions)
		for _, pat := range claudeAppHomes {
			// Only the part under root is a pattern, whatever root's name holds.
			found, _ := fs.Glob(os.DirFS(root), pat)
			for _, h := range found {
				out = append(out, filepath.Join(root, filepath.FromSlash(h)))
			}
		}
	}
	return out
}

// claudeAppRecord is the record the Claude app keeps beside h when h is the
// home of one of its sessions, or "".
func claudeAppRecord(p, h string) string {
	if p != "claude" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(h), "/")
	for _, pat := range claudeAppHomes {
		n := strings.Count(pat, "/") + 1
		if len(parts) <= n || parts[len(parts)-n-1] != claudeAppSessions {
			continue
		}
		if ok, _ := path.Match(pat, strings.Join(parts[len(parts)-n:], "/")); ok {
			return filepath.Dir(h) + ".json"
		}
	}
	return ""
}

// claudeApps is the app's records of the homes of p that are its sessions'
// homes, by home.
func claudeApps(p string, homes []string) map[string]claudeAppSession {
	apps := map[string]claudeAppSession{}
	for _, h := range homes {
		if rec := claudeAppRecord(p, h); rec != "" {
			apps[h] = readClaudeAppSession(rec)
		}
	}
	return apps
}

// claudeAppSession is what a run takes from the app's record of a session.
// The record also holds the session's title, first message, and system
// prompt, which are never decoded.
type claudeAppSession struct {
	Email   string   `json:"emailAddress"`
	Folders []string `json:"userSelectedFolders"`
}

// readClaudeAppSession reads the record in file. One that is missing or
// cannot be read names no account.
func readClaudeAppSession(file string) claudeAppSession {
	var s claudeAppSession
	body, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(body, &s) != nil {
		return claudeAppSession{}
	}
	return s
}

// account is the account the session ran under, as the app recorded it.
func (s claudeAppSession) account() string {
	if l := strings.TrimSpace(s.Email); l != "" {
		return l
	}
	return UnknownAccount
}

// project is the first folder the person gave the session.
func (s claudeAppSession) project() string {
	for _, f := range s.Folders {
		if f = strings.TrimSpace(f); f != "" {
			return f
		}
	}
	return claudeAppProject
}

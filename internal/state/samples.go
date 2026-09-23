package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Sample is one run: the quota each account reported and the token growth
// attributed to it since the previous run.
type Sample struct {
	At       time.Time       `json:"at"`
	Accounts []SampleAccount `json:"accounts"`
}

type SampleAccount struct {
	Provider string            `json:"provider"`
	Label    string            `json:"label"`
	QuotaAt  *time.Time        `json:"quota_at,omitempty"`
	Windows  []snapshot.Window `json:"windows,omitempty"`
	Growth   snapshot.Tokens   `json:"growth"`
}

const dayLayout = "2006-01-02"

// AppendSample adds a sample to that day's file.
func (d Dir) AppendSample(s Sample) error {
	dir := d.SamplesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, s.At.UTC().Format(dayLayout)+".jsonl")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// PruneSamples removes day files older than Retention.
func (d Dir) PruneSamples(now time.Time) error {
	cutoff := now.UTC().Add(-Retention).Format(dayLayout)
	entries, err := os.ReadDir(d.SamplesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		day, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || day >= cutoff {
			continue
		}
		if _, err := time.Parse(dayLayout, day); err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(d.SamplesDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// LoadSamples returns samples at or after since, oldest first.
// A damaged line is skipped.
func (d Dir) LoadSamples(since time.Time) ([]Sample, error) {
	entries, err := os.ReadDir(d.SamplesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	first := since.UTC().Format(dayLayout)
	var out []Sample
	for _, e := range entries {
		day, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || day < first {
			continue
		}
		f, err := os.Open(filepath.Join(d.SamplesDir(), e.Name()))
		if err != nil {
			return out, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var s Sample
			if json.Unmarshal(sc.Bytes(), &s) != nil || s.At.Before(since) {
				continue
			}
			out = append(out, s)
		}
		_ = f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

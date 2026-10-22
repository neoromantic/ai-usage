package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/neoromantic/ai-usage/internal/cost"
)

// pricesPath is the price list, from the repository's root.
const pricesPath = "internal/cost/prices.json"

// fixes are the differences -write sets: those with one price that could be
// the plan's. New plans, plans a page no longer shows, and pages that could
// not be read are left to a person.
func (r report) fixes() []difference {
	var out []difference
	for _, d := range r.Differs {
		if d.Fix > 0 {
			out = append(out, d)
		}
	}
	return out
}

// edit replaces the bytes of b from start to end with text.
type edit struct {
	start, end int
	text       string
}

// rewrite is the list b with the price of each fix and its updated day set
// to day, every other byte as it was: its formatting, the order of its keys,
// and each plan's source. It errs, and nothing should be written, when a fix
// names a plan the list has not, or has twice, or a price the plan has not.
func rewrite(b []byte, fixes []difference, day string) ([]byte, error) {
	if len(fixes) == 0 {
		return b, nil
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return nil, fmt.Errorf("day %q is not YYYY-MM-DD", day)
	}
	s := scanner{b: b, dec: json.NewDecoder(bytes.NewReader(b))}
	s.dec.UseNumber()
	edits, err := s.list(fixes, day)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pricesPath, err)
	}
	slices.SortFunc(edits, func(a, b edit) int { return b.start - a.start })
	out := slices.Clone(b)
	for _, e := range edits {
		out = slices.Concat(out[:e.start], []byte(e.text), out[e.end:])
	}
	if err := checkRewrite(out, fixes, day); err != nil {
		return nil, fmt.Errorf("%s as rewritten: %w", pricesPath, err)
	}
	return out, nil
}

// scanner finds where in the list's bytes each value it reads lies.
type scanner struct {
	b   []byte
	dec *json.Decoder
}

func (s scanner) delim(want json.Delim) error {
	t, err := s.dec.Token()
	if err != nil {
		return err
	}
	if t != want {
		return fmt.Errorf("found %v where %v belongs", t, want)
	}
	return nil
}

func (s scanner) key() (string, error) {
	t, err := s.dec.Token()
	if err != nil {
		return "", err
	}
	k, ok := t.(string)
	if !ok {
		return "", fmt.Errorf("found %v where a key belongs", t)
	}
	return k, nil
}

// value reads a string or a number and says where it lies, its quotes
// included.
func (s scanner) value() (any, edit, error) {
	t, err := s.dec.Token()
	if err != nil {
		return nil, edit{}, err
	}
	end := int(s.dec.InputOffset())
	var lit string
	switch v := t.(type) {
	case string:
		lit = strconv.Quote(v)
	case json.Number:
		lit = v.String()
	default:
		return nil, edit{}, fmt.Errorf("found %v where a string or a number belongs", t)
	}
	start := end - len(lit)
	if start < 0 || string(s.b[start:end]) != lit {
		return nil, edit{}, fmt.Errorf("cannot place %s in the file", lit)
	}
	return t, edit{start: start, end: end}, nil
}

func (s scanner) skip() error {
	var raw json.RawMessage
	return s.dec.Decode(&raw)
}

func (s scanner) list(fixes []difference, day string) ([]edit, error) {
	if err := s.delim('{'); err != nil {
		return nil, err
	}
	var edits []edit
	applied := make([]int, len(fixes))
	dated := false
	for s.dec.More() {
		k, err := s.key()
		if err != nil {
			return nil, err
		}
		switch k {
		case "updated":
			_, e, err := s.value()
			if err != nil {
				return nil, fmt.Errorf("updated: %w", err)
			}
			e.text = strconv.Quote(day)
			edits = append(edits, e)
			dated = true
		case "plans":
			if err := s.delim('['); err != nil {
				return nil, err
			}
			for i := 0; s.dec.More(); i++ {
				es, err := s.plan(fixes, applied)
				if err != nil {
					return nil, fmt.Errorf("plans[%d]: %w", i, err)
				}
				edits = append(edits, es...)
			}
			if err := s.delim(']'); err != nil {
				return nil, err
			}
		default:
			if err := s.skip(); err != nil {
				return nil, err
			}
		}
	}
	if !dated {
		return nil, errors.New("the list has no updated day")
	}
	for i, n := range applied {
		if n != 1 {
			return nil, fmt.Errorf("%s is in the list %d times", planName(fixes[i].Plan), n)
		}
	}
	return edits, nil
}

// plan reads one plan, and gives the edits of the fixes that are its.
func (s scanner) plan(fixes []difference, applied []int) ([]edit, error) {
	if err := s.delim('{'); err != nil {
		return nil, err
	}
	var provider, plan string
	at := map[string]edit{}
	for s.dec.More() {
		k, err := s.key()
		if err != nil {
			return nil, err
		}
		switch k {
		case "provider", "plan", "usd", "annual_usd":
			v, e, err := s.value()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			at[k] = e
			switch k {
			case "provider":
				provider, _ = v.(string)
			case "plan":
				plan, _ = v.(string)
			}
		default:
			if err := s.skip(); err != nil {
				return nil, err
			}
		}
	}
	if err := s.delim('}'); err != nil {
		return nil, err
	}
	var edits []edit
	for i, f := range fixes {
		if f.Plan.Provider != provider || f.Plan.Plan != plan {
			continue
		}
		applied[i]++
		k := "usd"
		if f.Annual {
			k = "annual_usd"
		}
		e, ok := at[k]
		if !ok {
			return nil, fmt.Errorf("%s has no %s", planName(f.Plan), k)
		}
		e.text = price(f.Fix)
		edits = append(edits, e)
	}
	return edits, nil
}

// price is a price as the list writes it: 25, or 24.5.
func price(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// checkRewrite reads the rewritten list back as a collector would, and
// checks it has the prices and the day it was given.
func checkRewrite(b []byte, fixes []difference, day string) error {
	if _, err := cost.ParsePrices(b); err != nil {
		return err
	}
	var l list
	if err := json.Unmarshal(b, &l); err != nil {
		return err
	}
	if l.Updated != day {
		return fmt.Errorf("updated is %q, want %q", l.Updated, day)
	}
	for _, f := range fixes {
		i := slices.IndexFunc(l.Plans, func(p listPlan) bool { return p.Provider == f.Plan.Provider && p.Plan == f.Plan.Plan })
		if i < 0 {
			return fmt.Errorf("%s is gone", planName(f.Plan))
		}
		got := l.Plans[i].USD
		if f.Annual {
			got = l.Plans[i].AnnualUSD
		}
		if math.Abs(got-f.Fix) > 1e-9 {
			return fmt.Errorf("%s is %v, want %v", planName(f.Plan), got, f.Fix)
		}
	}
	return nil
}

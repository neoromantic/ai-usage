// Command prices compares the price list, internal/cost/prices.json, with
// the providers' public pages, and prints what differs as Markdown: prices
// that changed, paid plans the list lacks, plans a page no longer shows, and
// pages it could not read. A page it cannot read or parse is reported as
// such, never as a change of price. It exits 0 when everything it checked
// matches, 1 when something differs or a page could not be read, and 2 when
// it could not run. The Prices workflow runs it every week and keeps one
// issue open while something differs.
//
// With -write, run from the repository's root, it also sets in
// internal/cost/prices.json each price that differs where the page shows
// one price that could be the plan's, and the list's updated day to today,
// leaving every other byte of the file as it was. New plans, plans a page no
// longer shows, and pages it could not read stay in the report for a person.
// The Prices workflow opens a pull request with what it wrote.
//
//	go run ./scripts/prices
//	go run ./scripts/prices -write
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/cost"
)

// list is the price list as this program reads it: cost.Plan without the
// page each price was checked on, which only this program needs.
type list struct {
	Updated string     `json:"updated"`
	Plans   []listPlan `json:"plans"`
}

type listPlan struct {
	Provider  string   `json:"provider"`
	Plan      string   `json:"plan"`
	Name      string   `json:"name"`
	USD       float64  `json:"usd"`
	AnnualUSD float64  `json:"annual_usd"`
	Was       []string `json:"was"`
	Source    string   `json:"source"`
}

// stores are the App Store pages that name each provider's individual plans.
// A provider whose list prices come from another page, as Claude's do since
// its App Store prices are higher, has its store page read only for plans
// the list lacks.
var stores = map[string]string{
	"claude": "https://apps.apple.com/us/app/claude-by-anthropic/id6473753684",
	"codex":  "https://apps.apple.com/us/app/chatgpt/id6448311069",
	"grok":   "https://apps.apple.com/us/app/grok/id6670324846",
}

const (
	userAgent = "ai-usage-prices (+https://github.com/neoromantic/ai-usage)"
	// maxPage is the most of a page read; the App Store's are about 1 MB.
	maxPage = 8 << 20
)

var client = &http.Client{Timeout: 30 * time.Second}

func main() {
	write := flag.Bool("write", false, "set the prices that differ in "+pricesPath+", from the repository's root")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	var l list
	if err := json.Unmarshal(cost.PricesJSON, &l); err != nil {
		fail("price list:", err)
	}
	var file os.FileInfo
	if *write {
		b, err := os.ReadFile(pricesPath)
		if err == nil && !bytes.Equal(b, cost.PricesJSON) {
			err = errors.New("it is not the list this program was built with")
		}
		if err == nil {
			file, err = os.Stat(pricesPath)
		}
		if err != nil {
			fail(pricesPath+":", err)
		}
	}
	pages := map[string]page{}
	for _, u := range pageURLs(l) {
		pages[u] = readPage(u)
	}
	r := compare(l, pages)
	if fixes := r.fixes(); *write && len(fixes) > 0 {
		b, err := rewrite(cost.PricesJSON, fixes, time.Now().UTC().Format(time.DateOnly))
		if err == nil {
			err = os.WriteFile(pricesPath, b, file.Mode().Perm())
		}
		if err != nil {
			fail("-write:", err)
		}
		fmt.Fprintf(os.Stderr, "prices: set %d prices in %s\n", len(fixes), pricesPath)
	}
	fmt.Print(r.markdown())
	if r.findings() {
		os.Exit(1)
	}
}

func fail(a ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"prices:"}, a...)...)
	os.Exit(2)
}

// pageURLs are the pages to read: each plan's source that has a reader, and
// each provider's store page.
func pageURLs(l list) []string {
	var out []string
	for _, p := range l.Plans {
		if readerFor(p.Source) != nil {
			out = append(out, p.Source)
		}
		if s, ok := stores[p.Provider]; ok {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// page is what a page offers, or why it could not be read.
type page struct {
	offers []offer
	err    error
}

func readPage(u string) page {
	read := readerFor(u)
	if read == nil {
		return page{err: errors.New("no reader for this page")}
	}
	b, err := fetch(u)
	if err != nil {
		return page{err: err}
	}
	offers, err := read(b)
	return page{offers: offers, err: err}
}

// fetch gets a page, once more after a network error or a server's error.
func fetch(u string) ([]byte, error) {
	var err error
	for try := range 2 {
		if try > 0 {
			time.Sleep(5 * time.Second)
		}
		var b []byte
		var retry bool
		b, retry, err = get(u)
		if err == nil || !retry {
			return b, err
		}
	}
	return nil, err
}

func get(u string) (b []byte, retry bool, err error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	if err != nil {
		return nil, true, err
	}
	if len(b) > maxPage {
		return nil, false, fmt.Errorf("page is over %d bytes", maxPage)
	}
	return b, false, nil
}

// readerFor is the reader of a page, or nil for a page no reader knows.
func readerFor(u string) reader {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" {
		return nil
	}
	switch {
	case pu.Host == "apps.apple.com":
		return readAppStore
	case pu.Host == "claude.com" && pu.Path == "/pricing":
		return readClaudePricing
	case pu.Host == "support.claude.com" && maxArticleRe.MatchString(pu.Path):
		return readClaudeMax
	}
	return nil
}

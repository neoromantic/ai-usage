package main

import (
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// offer is a plan as a page shows it: its name, its price in USD, and how
// often the price is charged, when the page says.
type offer struct {
	Name   string
	USD    float64
	Period period
}

type period int

const (
	anyPeriod period = iota // the page does not say
	monthly
	annual
)

// reader reads the offers of a page. It errs when the page is not one it
// knows, or shows no price it can read, so that a changed page is reported
// as unreadable, not as a change of price.
type reader func(page []byte) ([]offer, error)

var (
	serverDataRe = regexp.MustCompile(`(?s)<script[^>]*\bid="serialized-server-data"[^>]*>(.*?)</script>`)
	usdRe        = regexp.MustCompile(`^\$(\d{1,3}(?:,\d{3})*|\d+)(\.\d{1,2})?$`)
	periodRe     = regexp.MustCompile(`(?i)\s*(?:-\s*|\(\s*)(monthly|annual|annually|yearly)\s*\)?\s*$`)
)

// readAppStore reads the In-App Purchases of an App Store app page, from the
// data the page is built from: the ten purchases the store lists, each a
// name and a price.
func readAppStore(b []byte) ([]offer, error) {
	m := serverDataRe.FindSubmatch(b)
	if m == nil {
		return nil, errors.New("no serialized-server-data on the page")
	}
	var data any
	if err := json.Unmarshal(m[1], &data); err != nil {
		return nil, errors.New("serialized-server-data is not JSON")
	}
	var offers []offer
	found := false
	walk(data, func(o map[string]any) {
		if o["title"] != "In-App Purchases" {
			return
		}
		found = true
		items, _ := o["items"].([]any)
		for _, it := range items {
			item, _ := it.(map[string]any)
			name, _ := item["leadingText"].(string)
			price, _ := item["trailingText"].(string)
			usd, ok := parseUSD(price)
			if name == "" || !ok {
				continue
			}
			o := offer{Name: strings.TrimSpace(name), USD: usd}
			if p := periodRe.FindStringSubmatchIndex(o.Name); p != nil {
				switch strings.ToLower(o.Name[p[2]:p[3]]) {
				case "monthly":
					o.Period = monthly
				default:
					o.Period = annual
				}
				o.Name = strings.TrimSpace(o.Name[:p[0]])
			}
			if !slices.Contains(offers, o) {
				offers = append(offers, o)
			}
		}
	})
	switch {
	case !found:
		return nil, errors.New("no In-App Purchases on the page")
	case len(offers) == 0:
		return nil, errors.New("no In-App Purchase with a price in USD")
	}
	return offers, nil
}

// walk calls f on every object in v.
func walk(v any, f func(map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		f(v)
		for _, x := range v {
			walk(x, f)
		}
	case []any:
		for _, x := range v {
			walk(x, f)
		}
	}
}

// parseUSD reads a price such as "$19.99" or "$1,200.00".
func parseUSD(s string) (float64, bool) {
	m := usdRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "")+m[2], 64)
	return n, err == nil
}

var (
	proRe     = regexp.MustCompile(`\bPro\b`)
	maxRe     = regexp.MustCompile(`\bMax\b`)
	billedRe  = regexp.MustCompile(`\$(\d+(?:\.\d{2})?) if billed monthly`)
	upFrontRe = regexp.MustCompile(`\$(\d+(?:\.\d{2})?) billed up front`)
)

// readClaudePricing reads Pro's prices off claude.com/pricing: the card from
// a heading "Pro" to the next "Max", which says "$N if billed monthly" and
// "$N billed up front" for a year. Max's card gives only its lowest price,
// so Max is read from the support article that gives each tier's.
func readClaudePricing(b []byte) ([]offer, error) {
	text := pageText(b)
	for _, loc := range proRe.FindAllStringIndex(text, -1) {
		card := text[loc[1]:]
		if m := maxRe.FindStringIndex(card); m != nil {
			card = card[:m[0]]
		}
		if len(card) > 600 {
			card = card[:600]
		}
		m := billedRe.FindStringSubmatch(card)
		if m == nil {
			continue
		}
		out := []offer{{Name: "Pro", USD: atof(m[1]), Period: monthly}}
		if m := upFrontRe.FindStringSubmatch(card); m != nil {
			out = append(out, offer{Name: "Pro", USD: atof(m[1]), Period: annual})
		}
		return out, nil
	}
	return nil, errors.New(`no "Pro" card that says "$N if billed monthly"`)
}

var (
	maxArticleRe = regexp.MustCompile(`/articles/\d+-what-is-the-max-plan$`)
	maxTierRe    = regexp.MustCompile(`(?i)\b(Max \d+x)\s*:\s*\$(\d+(?:\.\d{2})?)\s*(?:per|a|/)\s*month`)
)

// readClaudeMax reads the Max tiers off Claude's support article on the Max
// plan, which lists them as "Max 5x: $100 per month".
func readClaudeMax(b []byte) ([]offer, error) {
	var out []offer
	for _, m := range maxTierRe.FindAllStringSubmatch(pageText(b), -1) {
		o := offer{Name: m[1], USD: atof(m[2]), Period: monthly}
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return nil, errors.New(`no "Max Nx: $N per month" on the page`)
	}
	return out, nil
}

func atof(s string) float64 {
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

var (
	codeRe = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>|<noscript\b.*?</noscript>`)
	tagRe  = regexp.MustCompile(`<[^>]*>`)
)

// pageText is a page's visible text, its words separated by single spaces.
func pageText(b []byte) string {
	s := codeRe.ReplaceAllString(string(b), " ")
	s = html.UnescapeString(tagRe.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

package cost

import (
	"encoding/json"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/state"
)

// Where a price list came from.
const (
	SourceBuiltin = "built in"
	SourceRelay   = "relay"
)

// FetchEvery is how often a collection asks the relay for its price list,
// whether or not the last ask got one.
const FetchEvery = 24 * time.Hour

// pricesFile keeps the relay's price list in the state folder.
const pricesFile = "prices.json"

// Fetched is the relay's price list as the last ask that got one found it,
// and when the relay was last asked.
type Fetched struct {
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	TriedAt   time.Time `json:"tried_at,omitzero"`
	// List is the list as the relay sent it, read again each time with
	// this build's ParsePrices.
	List json.RawMessage `json:"list,omitempty"`
}

// LoadFetched reads the relay's cached price list. A missing or damaged
// file is no list.
func LoadFetched(dir state.Dir) Fetched {
	var f Fetched
	b, err := os.ReadFile(dir.Path(pricesFile))
	if err != nil || json.Unmarshal(b, &f) != nil {
		return Fetched{}
	}
	return f
}

// FetchDue says the relay's price list is to be asked for again at now: it
// was last asked FetchEvery or more ago, or never, or the clock went back.
func FetchDue(dir state.Dir, now time.Time) bool {
	f := LoadFetched(dir)
	age := now.Sub(f.TriedAt)
	return f.TriedAt.IsZero() || age < 0 || age >= FetchEvery
}

// SaveFetched records an ask of the relay at now that answered body, or
// failed with err. A list this build cannot read keeps the one before, as
// a failure does: either is tried again after FetchEvery.
func SaveFetched(dir state.Dir, body []byte, err error, now time.Time) error {
	f := LoadFetched(dir)
	f.TriedAt = now.UTC()
	if err == nil {
		if _, perr := ParsePrices(body); perr == nil {
			f.List, f.FetchedAt = json.RawMessage(body), now.UTC()
		}
	}
	b, merr := json.Marshal(f)
	if merr != nil {
		return merr
	}
	return fsutil.WriteFile(dir.Path(pricesFile), b, 0o600)
}

// Current is the price list in use and where it came from: the relay's
// cached list when it was updated after the built-in one, else the
// built-in one. A relay that runs an older release cannot take prices back.
func Current(dir state.Dir) (Prices, string) {
	return Choose(Builtin(), LoadFetched(dir))
}

// Choose is the list of the two that was updated later, the built-in one
// when they were updated the same day, and where it came from.
func Choose(built Prices, f Fetched) (Prices, string) {
	if len(f.List) == 0 {
		return built, SourceBuiltin
	}
	fetched, err := ParsePrices(f.List)
	if err != nil || fetched.Updated <= built.Updated {
		return built, SourceBuiltin
	}
	return fetched, SourceRelay
}

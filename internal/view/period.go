package view

// Period is the span of tokens the matrix, USAGE, and PROJECTS show.
type Period int

const (
	Week    Period = iota // 7d, the default
	Today                 // the report's UTC day
	Month                 // 30d
	Quarter               // 90d
)

// Periods are the periods in the order `p` steps through them.
var Periods = []Period{Today, Week, Month, Quarter}

func (p Period) String() string {
	switch p {
	case Today:
		return "today"
	case Month:
		return "30d"
	case Quarter:
		return "90d"
	default:
		return "7d"
	}
}

// Of is the period's tokens in u.
func (p Period) Of(u Usage) int64 {
	switch p {
	case Today:
		return u.Today
	case Month:
		return u.Month
	case Quarter:
		return u.Quarter
	default:
		return u.Week
	}
}

// share is the period's part in s.
func (p Period) share(s Share) *float64 {
	switch p {
	case Today:
		return s.Today
	case Month:
		return s.Month
	case Quarter:
		return s.Quarter
	default:
		return s.Week
	}
}

// Next is the period after p, back to today after 90d.
func (p Period) Next() Period {
	for i, q := range Periods {
		if q == p {
			return Periods[(i+1)%len(Periods)]
		}
	}
	return Week
}

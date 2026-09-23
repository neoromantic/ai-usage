// Package view turns the collector's state into the two views: versioned JSON
// for an agent and console text for a person.
package view

import (
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// SchemaVersion is the agent JSON contract. A field may change meaning only
// with a new version.
const SchemaVersion = 3

const (
	// StaleAfter marks a quota reading as old in both views.
	StaleAfter = 6 * time.Hour
	// SilentAfter marks a device that has not reported for a day.
	SilentAfter = 24 * time.Hour
)

// Report is everything both views show. The console draws it as one page:
// what needs attention, the subscriptions, the devices against the
// subscriptions, and this device's projects.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Collector     Collector `json:"collector"`
	// Attention is what needs attention now, most urgent first.
	Attention []Attention `json:"attention"`
	// Providers are this device's sources and accounts.
	Providers []Provider `json:"providers"`
	// Projects are this device's projects over every account, most tokens
	// in the last 7 days first.
	Projects []Project `json:"projects"`
	Team     Team      `json:"team"`
}

type Collector struct {
	Version       string     `json:"version"`
	Device        string     `json:"device"`
	DeviceLabel   string     `json:"device_label"`
	OSUser        string     `json:"os_user"`
	Team          string     `json:"team"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *string    `json:"last_error"`
	LastErrorAt   *time.Time `json:"last_error_at"`
	Relay         Relay      `json:"relay"`
	Schedule      Schedule   `json:"schedule"`
	Update        Update     `json:"update"`
}

type Relay struct {
	URL        *string    `json:"url"`
	LastPushAt *time.Time `json:"last_push_at"`
	LastPullAt *time.Time `json:"last_pull_at"`
	Pending    bool       `json:"pending"`
	LastError  *string    `json:"last_error"`
}

type Schedule struct {
	Registered bool    `json:"registered"`
	Foreground bool    `json:"foreground"`
	Error      *string `json:"error"`
}

type Update struct {
	CheckedAt *time.Time `json:"checked_at"`
	Latest    *string    `json:"latest"`
	Staged    *string    `json:"staged"`
	Error     *string    `json:"error"`
}

// Attention kinds, most urgent first.
const (
	AttentionOut    = "out"    // a window is at 100%
	AttentionOver   = "over"   // a window will run out before it resets
	AttentionError  = "error"  // a device's collector or one of its harnesses fails
	AttentionSilent = "silent" // a device has not reported for a day
	AttentionOld    = "old"    // devices run an older release
	AttentionUnder  = "under"  // past half of a window, its forecast is under 50%
)

// Attention is one thing that needs attention. The fields that apply depend
// on Kind.
type Attention struct {
	Kind string `json:"kind"`
	// Provider, Account, and Name are the account of an out, over, or under
	// window: its label and its short name.
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	Name     string `json:"name,omitempty"`
	// Window names the window when it is not the account's main one.
	Window string `json:"window,omitempty"`
	// Devices is the device of an error or silence, or every device on an
	// old release.
	Devices []string `json:"devices,omitempty"`
	// At is when an out window resets, when an over window runs out, or
	// when a silent device last reported.
	At *time.Time `json:"at,omitempty"`
	// ResetsAt is when an over or under window resets.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Percent is an over or under window's forecast at its reset.
	Percent *float64 `json:"percent,omitempty"`
	// ReadingAge is how old the reading is, in seconds, when it is stale.
	ReadingAge int64 `json:"reading_age_seconds,omitempty"`
	// Message is an error's text, or the newest release for old devices.
	Message string `json:"message,omitempty"`
}

type Provider struct {
	Provider string    `json:"provider"`
	Status   string    `json:"status"`
	Error    *string   `json:"error"`
	Homes    []string  `json:"homes"`
	Accounts []Account `json:"accounts"`
}

// Account is one account on this device.
type Account struct {
	Label string `json:"label"`
	// Name is the account's short name: the one the team gave it with
	// `ai-usage alias`, else the part of an email before the @, or the first
	// 8 characters of an id. Two that would be the same within a provider are
	// both the full label.
	Name    string `json:"name"`
	Current bool   `json:"current"`
	// Home is the harness home the account is logged in to now.
	Home string  `json:"home,omitempty"`
	Plan *string `json:"plan"`
	// State is the worst state of the account's windows that limit it: out,
	// over, tight, ok, or under, else unknown.
	State        string          `json:"state"`
	Quota        *Quota          `json:"quota"`
	Link         *Link           `json:"link"`
	Sessions     int             `json:"sessions"`
	Tokens       snapshot.Tokens `json:"tokens"`
	Usage        Usage           `json:"usage"`
	Days         []int64         `json:"days"`
	LinkedUsage  []LinkedUsage   `json:"linked_usage"`
	LastActiveAt *time.Time      `json:"last_active_at"`
	Projects     []Project       `json:"projects"`
}

// Link names the account of another provider an account is assumed to bill
// through. On a team account the label is empty when the reading it shares
// matched no account, or several, on the device that sent it.
type Link struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// LinkedUsage is what an account of another provider spent through this one.
// Those tokens are that account's, never added to this one's. A team entry
// also names that account and the devices it ran on.
type LinkedUsage struct {
	Provider string          `json:"provider"`
	Label    string          `json:"label,omitempty"`
	Devices  []string        `json:"devices,omitempty"`
	Sessions int             `json:"sessions"`
	Tokens   snapshot.Tokens `json:"tokens"`
}

// Quota is an account's reading. In the team view each window is the newest
// reading of it from any device, so windows can differ in age.
type Quota struct {
	// ObservedAt is the newest window's reading time.
	ObservedAt time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Stale      bool      `json:"stale"`
	Source     string    `json:"source,omitempty"`
	// From names the provider whose linked account took the reading.
	From string `json:"from,omitempty"`
	// Device is the device whose reading is newest.
	Device  string   `json:"device,omitempty"`
	Windows []Window `json:"windows"`
}

// Forecast states. A window at 100% is out; otherwise the state is the band
// its forecast falls in. Unknown is a window with no forecast: it reset
// since it was read, its length or reset time is unknown, or less than a
// tenth of it has passed and it is not over.
const (
	StateOut     = "out"
	StateOver    = "over"
	StateTight   = "tight"
	StateOK      = "ok"
	StateUnder   = "under"
	StateUnknown = "unknown"
)

type Window struct {
	Name     string     `json:"name"`
	Percent  float64    `json:"percent"`
	ResetsAt *time.Time `json:"resets_at"`
	Minutes  int        `json:"minutes,omitempty"`
	// Main is the account's main window, the weekly one where it has one.
	Main       bool      `json:"main"`
	ObservedAt time.Time `json:"observed_at"`
	// Stale is a reading older than 6 hours.
	Stale bool `json:"stale"`
	// Reset says the window reset since it was read, so how full it is now
	// is not known.
	Reset    bool      `json:"reset"`
	State    string    `json:"state"`
	Forecast *Forecast `json:"forecast"`
}

// Forecast is how full a window will be at its reset if it is used from now
// on at its average pace so far.
type Forecast struct {
	// Percent is the forecast, as a whole percent up to 999.
	Percent float64 `json:"percent"`
	// Elapsed is the share of the window that had passed at the reading,
	// from 0 to 1.
	Elapsed float64 `json:"elapsed"`
	// RunsOutAt is when the window reaches 100% at that pace, when the
	// forecast is over 100%.
	RunsOutAt *time.Time `json:"runs_out_at"`
}

// Usage is input plus output tokens, cache left out, in the report's
// periods. Days are UTC days and each period ends with the report's: today
// is the report's UTC day, and 7d, 30d, and 90d are that many UTC days up to
// and including it.
type Usage struct {
	Today   int64 `json:"today"`
	Week    int64 `json:"7d"`
	Month   int64 `json:"30d"`
	Quarter int64 `json:"90d"`
}

// Project is usage in one working directory. On this device's list it is
// every account's; under an account, that account's.
type Project struct {
	Path     string          `json:"path"`
	Sessions int             `json:"sessions"`
	Tokens   snapshot.Tokens `json:"tokens"`
	Usage    Usage           `json:"usage"`
	// Providers are the harnesses that used it, most tokens first.
	Providers    []string   `json:"providers,omitempty"`
	LastActiveAt *time.Time `json:"last_active_at"`
}

type Team struct {
	PulledAt *time.Time `json:"pulled_at"`
	// Latest is the newest collector release any device in the team runs,
	// or the newest this device's update check saw, whichever is newer.
	Latest    *string        `json:"latest_version"`
	Devices   []TeamDevice   `json:"devices"`
	Providers []TeamProvider `json:"providers"`
	Matrix    Matrix         `json:"matrix"`
}

type TeamDevice struct {
	Device           string     `json:"device"`
	Label            string     `json:"label"`
	OSUser           string     `json:"os_user"`
	This             bool       `json:"this_device"`
	CollectorVersion string     `json:"collector_version"`
	CollectedAt      time.Time  `json:"collected_at"`
	AgeSeconds       int64      `json:"age_seconds"`
	LastSuccessAt    *time.Time `json:"last_success_at"`
	LastError        *string    `json:"last_error"`
	Sources          []Source   `json:"sources"`
	// Error is what fails on the device now: a source's error, named after
	// its provider, else the last run's error when it is newer than the
	// last success.
	Error *string `json:"error"`
	// Silent is a device that has not reported for a day.
	Silent bool `json:"silent"`
	// Old is a device on an older release than the team's newest.
	Old   bool  `json:"old"`
	Usage Usage `json:"usage"`
}

type Source struct {
	Provider string  `json:"provider"`
	Status   string  `json:"status"`
	Error    *string `json:"error"`
}

type TeamProvider struct {
	Provider string `json:"provider"`
	// Accounts are in the order the report lists them: the worst state
	// first (out, over, tight, ok, under, then unknown), ties to the one
	// with less left, then by label.
	Accounts []TeamAccount `json:"accounts"`
}

// TeamAccount adds tokens across devices. Each window of the quota is the
// newest reading any device has of it; percentages are never added.
type TeamAccount struct {
	Label string `json:"label"`
	Name  string `json:"name"`
	// Alias is the name the team gave the account, when it has one.
	Alias *string `json:"alias"`
	// Subscription is an account with a quota of its own: Claude, Codex, and
	// Grok. Hermes is a harness: what it spends through a login is that
	// login's, and its other accounts, such as API keys, have no quota.
	Subscription bool `json:"subscription"`
	// Current says the account is logged in on this device.
	Current bool     `json:"current"`
	Devices []string `json:"devices"`
	Plan    *string  `json:"plan"`
	State   string   `json:"state"`
	Quota   *Quota   `json:"quota"`
	Link    *Link    `json:"link"`
	// Sessions and Tokens are over the 90 days the devices keep.
	Sessions int             `json:"sessions"`
	Tokens   snapshot.Tokens `json:"tokens"`
	Usage    Usage           `json:"usage"`
	// Users counts the devices with tokens on the account since its main
	// window began, or in the last 7 days when it has none, what linked
	// accounts spent through it included. Busiest is the one with the most.
	Users        int           `json:"users"`
	Busiest      *string       `json:"busiest"`
	LastActiveAt *time.Time    `json:"last_active_at"`
	PerDevice    []DeviceUsage `json:"per_device"`
	LinkedUsage  []LinkedUsage `json:"linked_usage"`
}

// DeviceUsage is one device's share of a team account.
type DeviceUsage struct {
	Device       string          `json:"device"`
	DeviceID     string          `json:"device_id"`
	Current      bool            `json:"current"`
	Sessions     int             `json:"sessions"`
	Tokens       snapshot.Tokens `json:"tokens"`
	Usage        Usage           `json:"usage"`
	LastActiveAt *time.Time      `json:"last_active_at"`
}

// Matrix is who spends what: every team device against every subscription,
// and the tokens that have no subscription.
type Matrix struct {
	// Columns are the subscriptions, grouped by provider in the order of
	// the team's providers and accounts, then one column per provider for
	// the tokens with no quota.
	Columns []Column `json:"columns"`
	// Rows are the devices, the most tokens in the last 7 days first.
	Rows []Row `json:"rows"`
}

type Column struct {
	Provider string `json:"provider"`
	// Label is the subscription's account, empty in a no-quota column.
	Label   string `json:"label,omitempty"`
	Name    string `json:"name"`
	NoQuota bool   `json:"no_quota"`
	State   string `json:"state"`
	// Percent is how full the subscription's main window is, when that is
	// known; the share mode splits it between the devices.
	Percent *float64 `json:"percent"`
	Usage   Usage    `json:"usage"`
	// WindowTokens is the team's tokens since the main window began.
	WindowTokens int64 `json:"window_tokens"`
}

type Row struct {
	Device   string `json:"device"`
	DeviceID string `json:"device_id"`
	// Cells has one entry per column.
	Cells []Cell `json:"cells"`
	Usage Usage  `json:"usage"`
}

type Cell struct {
	Usage Usage `json:"usage"`
	// WindowTokens is the device's tokens since the column's main window
	// began.
	WindowTokens int64 `json:"window_tokens"`
	// Share estimates how much of the column's window the device used, in
	// percent: its tokens since the window began over the team's, times how
	// full the window is. A column's shares add up to its Percent.
	Share *float64 `json:"share"`
}

// Input is everything a report is built from.
type Input struct {
	Version  string
	RelayURL string
	Config   state.Config
	State    *state.State
	Key      *team.Key
	Doc      snapshot.Doc
	Team     collect.TeamCache
	Hostname string
	OSUser   string
	Now      time.Time
}

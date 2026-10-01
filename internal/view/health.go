package view

import (
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// staleTeamRead is how old the last read of the team gets before the header
// warns: three of the reads scheduled runs make every 20 minutes.
const staleTeamRead = 3 * 20 * time.Minute

// health is how the collection, the relay, and the update are doing at now.
// Each takes the first status that applies, else ok.
func health(c Collector, now time.Time) []Health {
	coll := Health{Item: HealthCollection, Status: HealthOK, State: LevelOK, At: timeOf(c.LastSuccessAt)}
	switch {
	case c.LastRunAt == nil:
		coll.Status, coll.State, coll.At = CollectionNever, LevelError, nil
	case failed(c):
		coll.Status, coll.State, coll.At = CollectionFailed, LevelError, timeOf(c.LastErrorAt)
	case !c.Schedule.Registered:
		coll.Status, coll.State = CollectionUnscheduled, LevelWarn
	}

	// The relay is as fresh as the older of the last push and the last
	// read: what the team sees of this device, or what this device sees of
	// the team.
	rl := c.Relay
	relay := Health{Item: HealthRelay, Status: HealthOK, State: LevelOK, At: older(rl.LastPushAt, rl.LastPullAt)}
	switch {
	case rl.URL == nil:
		relay.Status, relay.State, relay.At = RelayNone, LevelOff, nil
	case rl.LastError != nil:
		relay.Status, relay.State = RelayFailing, LevelError
	case rl.Pending || rl.LastPushAt == nil:
		relay.Status, relay.State = RelayPending, LevelWarn
	case c.Team != "" && (rl.LastPullAt == nil || now.Sub(*rl.LastPullAt) > staleTeamRead):
		relay.Status, relay.State = RelayStale, LevelWarn
	}

	up := c.Update
	update := Health{Item: HealthUpdate, Status: HealthOK, State: LevelOK, At: timeOf(up.CheckedAt)}
	switch {
	case selfupdate.Dev(c.Version):
		update.Status, update.State = UpdateDev, LevelOff
	case up.Staged != nil:
		update.Status, update.State, update.Release = UpdateStaged, LevelInfo, up.Staged
	case up.Error != nil:
		update.Status, update.State = UpdateFailed, LevelError
	case up.CheckedAt == nil:
		update.Status, update.State = UpdateUnchecked, LevelOff
	case up.Latest != nil && selfupdate.Newer(*up.Latest, c.Version):
		update.Status, update.State, update.Release = UpdateAvailable, LevelWarn, up.Latest
	}
	return []Health{coll, relay, update}
}

// older is the older of two times, either of which can be missing, or nil.
func older(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return timeOf(b)
	case b == nil || a.Before(*b):
		return timeOf(a)
	}
	return timeOf(b)
}

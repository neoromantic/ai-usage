package view

import "github.com/neoromantic/ai-usage/internal/selfupdate"

// health is how the collection, the relay, and the update are doing. Each
// takes the first status that applies, else ok.
func health(c Collector) []Health {
	coll := Health{Item: HealthCollection, Status: HealthOK, State: LevelOK, At: timeOf(c.LastSuccessAt)}
	switch {
	case c.LastRunAt == nil:
		coll.Status, coll.State, coll.At = CollectionNever, LevelError, nil
	case failed(c):
		coll.Status, coll.State, coll.At = CollectionFailed, LevelError, timeOf(c.LastErrorAt)
	case !c.Schedule.Registered:
		coll.Status, coll.State = CollectionUnscheduled, LevelWarn
	}

	rl := c.Relay
	relay := Health{Item: HealthRelay, Status: HealthOK, State: LevelOK, At: timeOf(rl.LastPushAt)}
	switch {
	case rl.URL == nil:
		relay.Status, relay.State, relay.At = RelayNone, LevelOff, nil
	case rl.LastError != nil:
		relay.Status, relay.State = RelayFailing, LevelError
	case rl.Pending || rl.LastPushAt == nil:
		relay.Status, relay.State = RelayPending, LevelWarn
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

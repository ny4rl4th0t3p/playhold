package playhold

import (
	"slices"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// PlayerDisconnected is the event emitted when a seated player has been gone for the whole grace period, set by
// ServerConfig.DisconnectGrace. The player keeps their seat, because the game decides what being away means. A connection
// that drops and returns in time emits nothing. Player, like rc.Player, is the player who left.
type PlayerDisconnected struct {
	Player PlayerID
}

// PlayerReturned is the event emitted when a player comes back after their PlayerDisconnected. Player, like rc.Player, is
// the player who returned.
type PlayerReturned struct {
	Player PlayerID
}

// graceTimer is how long a player who lost their connection has left to come back.
type graceTimer struct {
	player PlayerID
	due    time.Time
}

// roomSessions is what the lobby tells each room it makes about its players.
type roomSessions struct {
	grace    time.Duration // zero tracks no connections
	inactive time.Duration // zero never closes the room for inactivity
	admit    func() bool   // whether a new player may take a seat now; nil always allows
}

// startSessions turns on the grace period. Every seated player starts one, because a room that has just been restored
// has no connections yet, and anyone who doesn't come back has to be noticed. It's the room's first input, so it can't
// be refused for a full queue.
func (r *Room) startSessions(s roomSessions) {
	_ = r.submit(r.sessionsInput(s))
}

// sessionsInput is a room's first turn, which also starts its recording's segment.
func (r *Room) sessionsInput(s roomSessions) input {
	return input{noViews: true, run: func(_ *RoomContext) error {
		r.inactive = s.inactive
		r.admit = s.admit
		r.playerActive() // the clock starts now, also for a room restored with nobody in it yet
		r.grace = s.grace
		for _, player := range r.seats {
			if !slices.Contains(r.away, player) {
				r.startGrace(player)
			}
		}
		r.startSegment(s)
		return nil
	}}
}

// startGrace keeps r.graces soonest first, since every grace period is the same length and starts at the current turn.
func (r *Room) startGrace(player PlayerID) {
	if r.grace > 0 {
		r.graces = append(r.graces, graceTimer{player: player, due: r.now.Add(r.grace)})
	}
}

// playerBack ends the player's absence. The game only hears about it if it heard they had gone.
func (r *Room) playerBack(rc *RoomContext, player PlayerID) {
	r.graces = slices.DeleteFunc(r.graces, func(g graceTimer) bool { return g.player == player })
	if at := slices.Index(r.away, player); at >= 0 {
		r.away = slices.Delete(r.away, at, at+1)
		rc.Emit(PlayerReturned{Player: player})
	}
}

// playerActive pushes back the room's close for inactivity. Only what players do counts: messages and joining or coming
// back, not timers or disconnects.
func (r *Room) playerActive() {
	if r.inactive > 0 {
		r.inactiveDue = r.now.Add(r.inactive)
	}
}

// fireGrace runs a turn for the soonest grace period, and reports whether that turn panicked. The last snapshot doesn't
// have the player as away, so a handler that crashes the room fires again after it's restored, until the room's restart
// limit closes it.
func (r *Room) fireGrace() (panicked bool) {
	player := r.graces[0].player
	r.graces = slices.Delete(r.graces, 0, 1)
	return r.turn(r.graceInput(player))
}

// graceInput is the turn of a grace period that has ended, already removed from the pending ones.
func (r *Room) graceInput(player PlayerID) input {
	return input{player: player, timerEvent: "PlayerDisconnected", run: func(rc *RoomContext) error {
		if r.recorder != nil {
			r.record(&wire.RecordedTurn{Input: &wire.RecordedTurn_Grace{Grace: &wire.RecordedGrace{Player: string(player)}}})
		}
		if r.journal != nil {
			r.journalBegin(&JournalEntry{Player: player, Event: "PlayerDisconnected"})
		}
		r.away = append(r.away, player)
		rc.Emit(PlayerDisconnected{Player: player})
		return nil
	}}
}

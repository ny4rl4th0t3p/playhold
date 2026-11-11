// Package cards is a sample game, Crazy Eights for two players, built from three modules that know nothing of each
// other's packages. This file is the only place that sees all three, and wires them together.
package cards

import (
	"time"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/deck"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/hands"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/turns"
)

// Defaults for Options fields left at zero.
const (
	defaultTurnTime         = time.Minute
	defaultFinishedRoomTime = time.Minute
)

// Options sets the game's times. Zero values use the defaults.
type Options struct {
	// TurnTime is how long a player has for each turn, after which they draw and pass. The default is one minute.
	TurnTime time.Duration
	// FinishedRoomTime is how long a room stays open after its game ends. The default is one minute.
	FinishedRoomTime time.Duration
	// OldestClientSchema is the oldest page the server still plays with. The default is the current one, since there's
	// only been one so far.
	OldestClientSchema int
	// TurnsSnapshotVersion is the format the turns module saves games in, so a deploy that changes it can be tried on a
	// running game. The default is 1, and 2 is the only other.
	TurnsSnapshotVersion int
	// Record records every game, turn by turn, on a server with a recording folder, so a game can be replayed.
	Record bool
}

// ClientSchemaVersions is what a client built from this code says in its Hello.
func ClientSchemaVersions() map[string]uint32 {
	return map[string]uint32{turns.Module{}.Name(): uint32(pb.SchemaVersion_SCHEMA_VERSION_CURRENT)}
}

// NewRoomType returns the room type for one game, with the default times.
func NewRoomType() (*playhold.RoomType, error) {
	return NewRoomTypeWith(Options{})
}

// NewRoomTypeWith returns the room type for one game. The order matters: when a game starts, deck has to shuffle before
// hands deals.
func NewRoomTypeWith(options Options) (*playhold.RoomType, error) {
	if options.TurnTime <= 0 {
		options.TurnTime = defaultTurnTime
	}
	if options.FinishedRoomTime <= 0 {
		options.FinishedRoomTime = defaultFinishedRoomTime
	}
	if options.OldestClientSchema <= 0 {
		options.OldestClientSchema = int(pb.SchemaVersion_SCHEMA_VERSION_CURRENT)
	}
	return playhold.NewRoomType(playhold.RoomConfig{
		Name:       "crazy-eights",
		MaxPlayers: rules.Players,
		Record:     options.Record,
		Modules: []playhold.Module{
			deck.Module{},
			turns.Module{
				TurnTime:           options.TurnTime,
				FinishedRoomTime:   options.FinishedRoomTime,
				OldestClientSchema: options.OldestClientSchema,
				SnapshotVersion:    options.TurnsSnapshotVersion,
			},
			hands.Module{Deck: deck.Keeper{}, Turns: turns.Keeper{TurnTime: options.TurnTime}},
		},
	})
}

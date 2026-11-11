// Package roomtest connects playholdtest to the parts of a room that playhold keeps unexported, so they don't become
// part of every game's API. playhold sets New when it's initialized, and only playholdtest calls it.
package roomtest

import (
	"context"
	"log/slog"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// New starts a room for a test. Set by playhold.
var New func(cfg *Config) (Room, error)

// Replay plays the recording at path again against typ, a *playhold.RoomType. The error is for a file it can't read or
// a typ that isn't a room type; a game that plays differently is a Report with Diverged set. Set by playhold.
var Replay func(typ any, path string, logger *slog.Logger) (*Report, error)

// Report is how a replay went.
type Report struct {
	Turns         int
	Build         string
	Revision      string
	SchemaMatches bool
	Incomplete    bool
	Diverged      *Divergence
}

// Divergence is the first turn that didn't end as recorded.
type Divergence struct {
	Segment   int
	Turn      int // from 1; 0 is the room starting to run
	Input     any // a playhold.RecordedTurn, or nil for the start
	Reason    string
	WantHash  string
	GotHash   string
	WantError string
	GotError  string
}

// Config is what a test asks for. This package can't import playhold, so playhold's types are passed as any.
type Config struct {
	Type     any      // a *playhold.RoomType
	Seed     [32]byte // the zero seed picks a random one, as a server does
	Logger   *slog.Logger
	Grace    Timeout
	Inactive Timeout
	Timers   int // a playhold.TimersAfterRestart
}

// Timeout is a playhold.Timeout.
type Timeout struct {
	After time.Duration
	Off   bool
}

// Sink receives a player's views, on the room's goroutine. Its method must not block.
type Sink interface {
	SendState(state *wire.RoomState)
}

// Room is a room under test. The methods that take a result queue work for the room and return at once; the result
// runs on the room's goroutine once the work is done, and never if the room closes first.
type Room interface {
	// Seat seats the player, or reconnects one with a seat, and sends their views to the sink.
	Seat(player string, sink Sink, result func(error)) error
	// Leave is the player's connection through this sink dropping.
	Leave(player string, sink Sink)
	// Send delivers a message from the player, and its handler's error to result.
	Send(player, module string, id uint32, payload []byte, result func(error)) error
	// Do runs fn as a turn of the room. rc is a *playhold.RoomContext.
	Do(fn func(rc any), result func(error)) error
	// Restart saves the room as a shutdown would, and brings it back from that save as a room of typ, a
	// *playhold.RoomType. It returns once the new room is running, with nobody connected.
	Restart(typ any) error
	// Seed returns the seed of the room's random numbers: Config.Seed, or the random one picked for a zero seed.
	Seed() [32]byte
	// Closed reports whether the room has closed, and why, as a playhold.CloseReason.
	Closed() (reason int, closed bool)
	// Close shuts the room down, and returns once it has finished.
	Close(ctx context.Context) error
}

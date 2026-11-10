// Package store defines what a playhold server needs to keep across restarts. The memory and postgres packages
// implement it.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound means the store has no such record.
var ErrNotFound = errors.New("store: not found")

// RoomRecord is a room's latest snapshot. The store keeps Data as it is, without reading it.
type RoomRecord struct {
	ID       string
	Code     string
	RoomType string
	Data     []byte
}

// Incident is a turn that hurt the whole server: it was running when the process died, or it got stuck and kept running.
// The server counts them to turn off for a while a message that keeps doing it, and to keep a room that keeps doing it
// from coming back.
type Incident struct {
	// Build identifies the server's build. A new build starts with no incidents.
	Build string
	// Kind is "stuck" or "crashed".
	Kind   string
	Room   string
	Player string
	// Module and MessageID name a player's message. Event names the event of a timer or grace period instead.
	Module    string
	MessageID uint32
	Event     string
	At        time.Time
}

// Store keeps what has to outlive the process.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// Ping reports whether the store can be reached.
	Ping(ctx context.Context) error

	// SaveGuest records a guest's secret hash, replacing any earlier one, and that the guest was seen now.
	SaveGuest(ctx context.Context, player string, secretHash []byte) error
	// GuestSecretHash returns a guest's secret hash, or ErrNotFound.
	GuestSecretHash(ctx context.Context, player string) ([]byte, error)
	// TouchGuest records that a guest was seen at the given time. An unknown guest is fine, and changes nothing.
	TouchGuest(ctx context.Context, player string, at time.Time) error
	// DeleteGuestsUnseenSince deletes the guests last seen before the given time, and returns how many it deleted.
	DeleteGuestsUnseenSince(ctx context.Context, before time.Time) (int, error)

	// WriteRooms saves these rooms, replacing their earlier records, and deletes the others, all or nothing.
	WriteRooms(ctx context.Context, saves []RoomRecord, deletes []string) error
	// LoadRooms returns every saved room, in no particular order.
	LoadRooms(ctx context.Context) ([]RoomRecord, error)

	// SaveIncident records an incident.
	SaveIncident(ctx context.Context, incident *Incident) error
	// LoadIncidents returns every recorded incident, oldest first.
	LoadIncidents(ctx context.Context) ([]Incident, error)
	// DeleteIncidents deletes the incidents of every build but keepBuild, and those that happened before the given time.
	DeleteIncidents(ctx context.Context, keepBuild string, before time.Time) error
}

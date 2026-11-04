// Package store defines what a playhold server needs to keep across restarts. The memory and postgres packages
// implement it.
package store

import (
	"context"
	"errors"
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

// Store keeps what has to outlive the process.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// SaveGuest records a guest's secret hash, replacing any earlier one.
	SaveGuest(ctx context.Context, player string, secretHash []byte) error
	// GuestSecretHash returns a guest's secret hash, or ErrNotFound.
	GuestSecretHash(ctx context.Context, player string) ([]byte, error)

	// WriteRooms saves these rooms, replacing their earlier records, and deletes the others, all or nothing.
	WriteRooms(ctx context.Context, saves []RoomRecord, deletes []string) error
	// LoadRooms returns every saved room, in no particular order.
	LoadRooms(ctx context.Context) ([]RoomRecord, error)
}

// Package memory is a store.Store that forgets everything when the process ends. It's for development and tests.
package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// Store keeps everything in memory.
type Store struct {
	mu     sync.Mutex
	guests map[string][]byte
	rooms  map[string]store.RoomRecord
}

var _ store.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{guests: make(map[string][]byte), rooms: make(map[string]store.RoomRecord)}
}

// WriteRooms implements store.Store.
func (s *Store) WriteRooms(_ context.Context, saves []store.RoomRecord, deletes []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range saves {
		record := saves[i]
		record.Data = slices.Clone(record.Data)
		s.rooms[record.ID] = record
	}
	for _, id := range deletes {
		delete(s.rooms, id)
	}
	return nil
}

// LoadRooms implements store.Store.
func (s *Store) LoadRooms(_ context.Context) ([]store.RoomRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rooms := make([]store.RoomRecord, 0, len(s.rooms))
	for _, record := range s.rooms {
		record.Data = slices.Clone(record.Data)
		rooms = append(rooms, record)
	}
	return rooms, nil
}

// SaveGuest implements store.Store.
func (s *Store) SaveGuest(_ context.Context, player string, secretHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guests[player] = slices.Clone(secretHash)
	return nil
}

// GuestSecretHash implements store.Store.
func (s *Store) GuestSecretHash(_ context.Context, player string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, ok := s.guests[player]
	if !ok {
		return nil, store.ErrNotFound
	}
	return slices.Clone(hash), nil
}

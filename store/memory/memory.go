// Package memory is a store.Store that forgets everything when the process ends. It's for development and tests.
package memory

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// Store keeps everything in memory.
type Store struct {
	mu        sync.Mutex
	guests    map[string]guest
	rooms     map[string]store.RoomRecord
	incidents []store.Incident
}

type guest struct {
	secretHash []byte
	lastSeen   time.Time
}

var _ store.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{guests: make(map[string]guest), rooms: make(map[string]store.RoomRecord)}
}

// Ping implements store.Store.
func (*Store) Ping(_ context.Context) error { return nil }

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

// SaveIncident implements store.Store.
func (s *Store) SaveIncident(_ context.Context, incident *store.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incidents = append(s.incidents, *incident)
	return nil
}

// LoadIncidents implements store.Store.
func (s *Store) LoadIncidents(_ context.Context) ([]store.Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	incidents := slices.Clone(s.incidents)
	slices.SortStableFunc(incidents, func(a, b store.Incident) int { return a.At.Compare(b.At) })
	return incidents, nil
}

// DeleteIncidents implements store.Store.
func (s *Store) DeleteIncidents(_ context.Context, keepBuild string, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incidents = slices.DeleteFunc(s.incidents, func(incident store.Incident) bool {
		return incident.Build != keepBuild || incident.At.Before(before)
	})
	return nil
}

// SaveGuest implements store.Store.
func (s *Store) SaveGuest(_ context.Context, player string, secretHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guests[player] = guest{secretHash: slices.Clone(secretHash), lastSeen: time.Now()}
	return nil
}

// GuestSecretHash implements store.Store.
func (s *Store) GuestSecretHash(_ context.Context, player string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[player]
	if !ok {
		return nil, store.ErrNotFound
	}
	return slices.Clone(g.secretHash), nil
}

// TouchGuest implements store.Store.
func (s *Store) TouchGuest(_ context.Context, player string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.guests[player]; ok {
		g.lastSeen = at
		s.guests[player] = g
	}
	return nil
}

// DeleteGuestsUnseenSince implements store.Store.
func (s *Store) DeleteGuestsUnseenSince(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for player, g := range s.guests {
		if g.lastSeen.Before(before) {
			delete(s.guests, player)
			deleted++
		}
	}
	return deleted, nil
}

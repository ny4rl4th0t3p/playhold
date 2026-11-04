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
}

var _ store.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{guests: make(map[string][]byte)}
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

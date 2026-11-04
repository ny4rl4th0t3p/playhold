// Package storetest holds the tests every Store has to pass, so all implementations behave the same.
package storetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// Run tests a Store. newStore returns a new, empty store each time it's called.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()

	tests := []struct {
		name string
		test func(t *testing.T, s store.Store)
	}{
		{name: "unknown guest", test: unknownGuest},
		{name: "saved guest", test: savedGuest},
		{name: "saving again replaces", test: savingAgainReplaces},
		{name: "guests are separate", test: guestsAreSeparate},
		{name: "changing a returned hash changes nothing", test: returnedHashIsACopy},
		{name: "no rooms", test: noRooms},
		{name: "saved rooms", test: savedRooms},
		{name: "saving a room again replaces it", test: savingARoomAgainReplaces},
		{name: "deleted rooms", test: deletedRooms},
		{name: "saves and deletes together", test: savesAndDeletesTogether},
		{name: "deleting an unknown room is fine", test: deletingAnUnknownRoom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.test(t, newStore(t))
		})
	}
}

func room(id, data string) store.RoomRecord {
	return store.RoomRecord{ID: id, Code: "code-" + id, RoomType: "match", Data: []byte(data)}
}

func noRooms(t *testing.T, s store.Store) {
	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.Empty(t, got)
}

func savedRooms(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("a", "one"), room("b", "two")}, nil))

	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.ElementsMatch(t, []store.RoomRecord{room("a", "one"), room("b", "two")}, got)
}

func savingARoomAgainReplaces(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("a", "old")}, nil))
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("a", "new")}, nil))

	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []store.RoomRecord{room("a", "new")}, got)
}

func deletedRooms(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("a", "one"), room("b", "two")}, nil))
	require.NoError(t, s.WriteRooms(t.Context(), nil, []string{"a"}))

	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []store.RoomRecord{room("b", "two")}, got)
}

func savesAndDeletesTogether(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("a", "one")}, nil))
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{room("b", "two")}, []string{"a"}))

	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []store.RoomRecord{room("b", "two")}, got)
}

func deletingAnUnknownRoom(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), nil, []string{"nobody"}))
}

func unknownGuest(t *testing.T, s store.Store) {
	_, err := s.GuestSecretHash(t.Context(), "nobody")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func savedGuest(t *testing.T, s store.Store) {
	require.NoError(t, s.SaveGuest(t.Context(), "alice", []byte("hash")))

	got, err := s.GuestSecretHash(t.Context(), "alice")

	require.NoError(t, err)
	assert.Equal(t, []byte("hash"), got)
}

func savingAgainReplaces(t *testing.T, s store.Store) {
	require.NoError(t, s.SaveGuest(t.Context(), "alice", []byte("old")))
	require.NoError(t, s.SaveGuest(t.Context(), "alice", []byte("new")))

	got, err := s.GuestSecretHash(t.Context(), "alice")

	require.NoError(t, err)
	assert.Equal(t, []byte("new"), got)
}

func guestsAreSeparate(t *testing.T, s store.Store) {
	require.NoError(t, s.SaveGuest(t.Context(), "alice", []byte("a")))
	require.NoError(t, s.SaveGuest(t.Context(), "bob", []byte("b")))

	got, err := s.GuestSecretHash(t.Context(), "alice")

	require.NoError(t, err)
	assert.Equal(t, []byte("a"), got)
}

func returnedHashIsACopy(t *testing.T, s store.Store) {
	require.NoError(t, s.SaveGuest(t.Context(), "alice", []byte("hash")))
	first, err := s.GuestSecretHash(t.Context(), "alice")
	require.NoError(t, err)

	first[0] = 'X'

	second, err := s.GuestSecretHash(t.Context(), "alice")
	require.NoError(t, err)
	assert.Equal(t, []byte("hash"), second)
}

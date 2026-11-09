// Package storetest holds the tests every Store has to pass, so all implementations behave the same.
package storetest

import (
	"strconv"
	"testing"
	"time"

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
		{name: "ping", test: func(t *testing.T, s store.Store) { require.NoError(t, s.Ping(t.Context())) }},
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
		{name: "writing nothing is fine", test: writingNothing},
		{name: "a large batch, saving new and existing rooms and deleting others", test: largeBatch},
		{name: "no incidents", test: noIncidents},
		{name: "saved incidents, oldest first", test: savedIncidents},
		{name: "deleting incidents of other builds and old ones", test: deletingIncidents},
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

func writingNothing(t *testing.T, s store.Store) {
	require.NoError(t, s.WriteRooms(t.Context(), nil, nil))
	require.NoError(t, s.WriteRooms(t.Context(), []store.RoomRecord{}, []string{}))
}

// largeBatch is about the size the writer sends under load, with every kind of change at once.
func largeBatch(t *testing.T, s store.Store) {
	const rooms = 600
	id := func(i int) string { return "room-" + strconv.Itoa(i) }

	first := make([]store.RoomRecord, 0, rooms)
	for i := range rooms {
		first = append(first, room(id(i), "old"))
	}
	require.NoError(t, s.WriteRooms(t.Context(), first, nil))

	// The first half is deleted, the second half saved again, and as many new rooms saved.
	var saves []store.RoomRecord
	var deletes []string
	want := make([]store.RoomRecord, 0, rooms)
	for i := range rooms {
		if i < rooms/2 {
			deletes = append(deletes, id(i))
			continue
		}
		saves = append(saves, room(id(i), "new"))
		want = append(want, room(id(i), "new"))
	}
	for i := rooms; i < rooms+rooms/2; i++ {
		saves = append(saves, room(id(i), "added"))
		want = append(want, room(id(i), "added"))
	}
	require.NoError(t, s.WriteRooms(t.Context(), saves, deletes))

	got, err := s.LoadRooms(t.Context())

	require.NoError(t, err)
	assert.ElementsMatch(t, want, got)
}

// Times every store keeps exactly: whole seconds, in UTC.
var (
	early  = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	middle = early.Add(time.Minute)
	late   = middle.Add(time.Minute)
)

func incidentAt(build string, at time.Time) store.Incident {
	return store.Incident{
		Build: build, Kind: "stuck", Room: "room-1", Player: "alice", Module: "deck", MessageID: 4_000_000_000,
		Event: "", At: at,
	}
}

func noIncidents(t *testing.T, s store.Store) {
	got, err := s.LoadIncidents(t.Context())

	require.NoError(t, err)
	assert.Empty(t, got)
}

func savedIncidents(t *testing.T, s store.Store) {
	later, earlier := incidentAt("b1", late), incidentAt("b1", early)
	later.Kind, later.Module, later.MessageID, later.Event = "crashed", "", 0, "game.Tick"
	require.NoError(t, s.SaveIncident(t.Context(), &later))
	require.NoError(t, s.SaveIncident(t.Context(), &earlier))

	got, err := s.LoadIncidents(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []store.Incident{earlier, later}, got)
}

func deletingIncidents(t *testing.T, s store.Store) {
	old, kept, otherBuild := incidentAt("b2", early), incidentAt("b2", middle), incidentAt("b1", late)
	for _, incident := range []*store.Incident{&old, &kept, &otherBuild} {
		require.NoError(t, s.SaveIncident(t.Context(), incident))
	}

	require.NoError(t, s.DeleteIncidents(t.Context(), "b2", kept.At))

	got, err := s.LoadIncidents(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []store.Incident{kept}, got, "the one at the cutoff is kept")
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

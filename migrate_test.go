package playhold

import (
	"errors"
	"testing"
	"testing/synctest"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// A migration in these tests works on the number savedModule keeps.
func changeNumber(change func(int32) int32) func([]byte) ([]byte, error) {
	return func(old []byte) ([]byte, error) {
		msg := &addMsg{}
		if err := proto.Unmarshal(old, msg); err != nil {
			return nil, err
		}
		return proto.Marshal(&addMsg{Value: change(msg.GetValue())})
	}
}

func addTen(n int32) int32 { return n + 10 }

func double(n int32) int32 { return n * 2 }

// versioned is savedModule at some schema version, with these migrations.
func versioned(version int, migrations ...func(r *Registry)) registerFunc {
	return registerFunc{name: "saved", register: func(r *Registry) {
		State(r, savedKey, func() *counterState { return &counterState{} })
		Snapshot(r, savedKey, version,
			func(s *counterState) *addMsg { return &addMsg{Value: int32(s.value)} }, //nolint:gosec // small numbers in a test
			func(msg *addMsg) *counterState { return &counterState{value: int(msg.GetValue())} },
		)
		Handle(r, msgAdd, func(rc *RoomContext, msg *addMsg) error {
			savedKey.Get(rc).value += int(msg.GetValue())
			return nil
		})
		for _, migration := range migrations {
			migration(r)
		}
	}}
}

func step(from int, change func([]byte) ([]byte, error)) func(r *Registry) {
	return func(r *Registry) { Migrate(r, savedKey, from, change) }
}

// processAt is a server with the module at this version.
func processAt(t *testing.T, records store.Store, module Module) (*lobby, *Runtime) {
	t.Helper()
	w := newTestWriter(t, records)
	rt := newTestRuntime(t)
	return newLobby(rt, newTestRoomType(t, RoomConfig{Modules: []Module{module}}), testIdle, testMaxRooms, w), rt
}

// savedAt plays a room at a version, adds 3, and stops the server.
func savedAt(t *testing.T, records store.Store, module Module) {
	t.Helper()
	l, rt := processAt(t, records, module)
	room := enterRoom(t, l, testCode)
	require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: 3}))
	stopProcess(t, rt)
}

func storedData(t *testing.T, records store.Store) []byte {
	t.Helper()
	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	require.Len(t, rooms, 1)
	return rooms[0].Data
}

func TestRoomSavedAtAnOldVersionGoesThroughEachStepInOrder(t *testing.T) {
	tests := []struct {
		name    string
		version int
		want    int
	}{
		{name: "from the first version", version: 1, want: (3 + 10) * 2},
		{name: "from a middle version", version: 2, want: 3 * 2},
		{name: "already current", version: 3, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := memory.New()
				steps := []func(r *Registry){step(1, changeNumber(addTen)), step(2, changeNumber(double))}
				savedAt(t, records, versioned(tt.version, steps[:tt.version-1]...))

				l, _ := processAt(t, records, versioned(3, steps...))
				require.NoError(t, l.restoreAll(t.Context(), records))

				assert.Equal(t, tt.want, valueIn(t, enterRoom(t, l, testCode)))
			})
		})
	}
}

func TestMigratedRoomIsSavedAtTheNewVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		savedAt(t, records, versioned(1))

		l, _ := processAt(t, records, versioned(2, step(1, changeNumber(addTen))))
		require.NoError(t, l.restoreAll(t.Context(), records))
		room := enterRoom(t, l, testCode)
		require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: 1}))
		synctest.Wait()

		snapshot, value := savedRoom(t, records, room)
		assert.Equal(t, uint32(2), snapshot.GetModules()[0].GetVersion())
		assert.Equal(t, int32(14), value)
	})
}

func TestMigrationThatFailsKeepsTheOriginal(t *testing.T) {
	tests := []struct {
		name string
		step func([]byte) ([]byte, error)
	}{
		{name: "returns an error", step: func(_ []byte) ([]byte, error) { return nil, errors.New("can't") }},
		{name: "panics", step: func(_ []byte) ([]byte, error) { panic("boom") }},
		{name: "returns something unreadable", step: func(_ []byte) ([]byte, error) { return []byte{0xff}, nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := memory.New()
				savedAt(t, records, versioned(1))
				before := storedData(t, records)

				broken, _ := processAt(t, records, versioned(2, step(1, tt.step)))
				require.NoError(t, broken.restoreAll(t.Context(), records))
				synctest.Wait()

				assert.Empty(t, broken.rooms, "the room isn't restored")
				assert.Equal(t, before, storedData(t, records))

				// A fixed server gets the match back.
				fixed, _ := processAt(t, records, versioned(2, step(1, changeNumber(addTen))))
				require.NoError(t, fixed.restoreAll(t.Context(), records))

				assert.Equal(t, 13, valueIn(t, enterRoom(t, fixed, testCode)))
			})
		})
	}
}

func TestRoomSavedByANewerServerIsLeftAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		savedAt(t, records, versioned(2, step(1, changeNumber(addTen))))
		before := storedData(t, records)

		// A deploy rolled back to the older server.
		l, _ := processAt(t, records, versioned(1))
		require.NoError(t, l.restoreAll(t.Context(), records))
		synctest.Wait()

		assert.Empty(t, l.rooms)
		assert.Equal(t, before, storedData(t, records))
	})
}

func TestRoomWithAnImpossibleVersionIsNotRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		savedAt(t, records, versioned(1))

		snapshot := &wire.RoomSnapshot{}
		require.NoError(t, proto.Unmarshal(storedData(t, records), snapshot))
		snapshot.GetModules()[0].Version = 0
		data, err := proto.Marshal(snapshot)
		require.NoError(t, err)
		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		rooms[0].Data = data
		require.NoError(t, records.WriteRooms(t.Context(), rooms, nil))

		l, _ := processAt(t, records, versioned(1))
		require.NoError(t, l.restoreAll(t.Context(), records))

		assert.Empty(t, l.rooms)
	})
}

func TestMigrationWiringMistakes(t *testing.T) {
	change := changeNumber(addTen)
	otherKey := NewKey[counterState]("no-snapshot")

	tests := []struct {
		name   string
		module registerFunc
		want   string
	}{
		{name: "missing the only step", module: versioned(2), want: "no migration from version 1"},
		{name: "missing a middle step", module: versioned(3, step(1, change)), want: "no migration from version 2"},
		{name: "two steps from one version", module: versioned(2, step(1, change), step(1, changeNumber(double))), want: "two migrations"},
		{name: "a step from the current version", module: versioned(2, step(1, change), step(2, change)), want: "version 2"},
		{name: "a step from version zero", module: versioned(2, step(1, change), step(0, change)), want: "version 0"},
		{name: "no function", module: versioned(2, step(1, nil)), want: "has no function"},
		{
			name: "a state with no snapshot",
			module: registerFunc{name: "m", register: func(r *Registry) {
				State(r, otherKey, func() *counterState { return &counterState{} })
				Migrate(r, otherKey, 1, change)
			}},
			want: "no snapshot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRoomType(RoomConfig{Modules: []Module{tt.module}})

			require.ErrorContains(t, err, tt.want)
		})
	}
}

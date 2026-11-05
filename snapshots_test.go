package playhold

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const (
	savedVersion = 2

	msgPanic uint32 = iota + 10
	msgRefuseAfterAdding
)

var savedKey = NewKey[counterState]("saved")

var errRefused = errors.New("refused")

// savedModule is a module whose state is a number, which it knows how to save.
var savedModule = registerFunc{name: "saved", register: func(r *Registry) {
	State(r, savedKey, func() *counterState { return &counterState{} })
	Snapshot(r, savedKey, savedVersion,
		func(s *counterState) *addMsg { return &addMsg{Value: int32(s.value)} },
		func(msg *addMsg) *counterState { return &counterState{value: int(msg.GetValue())} },
	)
	Handle(r, msgAdd, func(rc *RoomContext, msg *addMsg) error {
		savedKey.Get(rc).value += int(msg.GetValue())
		return nil
	})
	Handle(r, msgRefuseAfterAdding, func(rc *RoomContext, msg *addMsg) error {
		savedKey.Get(rc).value += int(msg.GetValue())
		return errRefused
	})
	Handle(r, msgPanic, func(_ *RoomContext, _ *addMsg) error { panic("boom") })
}}

// writeCountingStore counts writes that reach the store.
type writeCountingStore struct {
	store.Store

	mu     sync.Mutex
	writes int
}

func (s *writeCountingStore) WriteRooms(ctx context.Context, saves []store.RoomRecord, deletes []string) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	return s.Store.WriteRooms(ctx, saves, deletes)
}

func (s *writeCountingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// gatedStore holds each write until the test releases it, and can make it fail.
type gatedStore struct {
	store.Store

	release chan error
}

func (s *gatedStore) WriteRooms(ctx context.Context, saves []store.RoomRecord, deletes []string) error {
	select {
	case err := <-s.release:
		if err != nil {
			return err
		}
		return s.Store.WriteRooms(ctx, saves, deletes)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newTestWriter(t *testing.T, records store.Store) *snapshotWriter {
	t.Helper()
	w := newSnapshotWriter(records, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		w.close(ctx)
	})
	return w
}

// newSavedRoom returns a room that saves to records, running under its own runtime.
func newSavedRoom(t *testing.T, records store.Store, cfg RoomConfig) (*Room, *Runtime) {
	t.Helper()
	w := newTestWriter(t, records) // first, so it closes after the runtime has shut down
	rt := newTestRuntime(t)
	cfg.Modules = append(cfg.Modules, savedModule)
	room, err := rt.createRoom(newTestRoomType(t, cfg), "room-code", w, nil)
	require.NoError(t, err)
	return room, rt
}

// savedRoom returns the room's stored snapshot, and the number in its module's state.
func savedRoom(t *testing.T, records store.Store, room *Room) (snapshot *wire.RoomSnapshot, value int32) {
	t.Helper()
	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	require.Len(t, rooms, 1)
	assert.Equal(t, string(room.ID()), rooms[0].ID)

	snapshot = &wire.RoomSnapshot{}
	require.NoError(t, proto.Unmarshal(rooms[0].Data, snapshot))
	require.Len(t, snapshot.GetModules(), 1)
	state := &addMsg{}
	require.NoError(t, proto.Unmarshal(snapshot.GetModules()[0].GetState(), state))
	return snapshot, state.GetValue()
}

func requireNoRooms(t *testing.T, records store.Store) {
	t.Helper()
	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	require.Empty(t, rooms)
}

func TestRoomIsSavedWhenCreatedAndAfterEachTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		room, _ := newSavedRoom(t, records, RoomConfig{Name: "match"})
		synctest.Wait()

		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		require.Len(t, rooms, 1)
		assert.Equal(t, "room-code", rooms[0].Code)
		assert.Equal(t, "match", rooms[0].RoomType)

		_, value := savedRoom(t, records, room)
		assert.Equal(t, int32(0), value)

		require.NoError(t, seatAndWait(t, room, "alice"))
		require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: 3}))
		synctest.Wait()

		snapshot, value := savedRoom(t, records, room)
		assert.Equal(t, int32(3), value)
		assert.Equal(t, []string{"alice"}, snapshot.GetSeats())
		assert.Equal(t, uint32(savedVersion), snapshot.GetModules()[0].GetVersion())
		assert.Equal(t, "saved", snapshot.GetModules()[0].GetModule())
	})
}

func TestRefusedMessageIsStillSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		room, _ := newSavedRoom(t, records, RoomConfig{})

		require.ErrorIs(t, dispatchAndWait(t, room, "saved", msgRefuseAfterAdding, &addMsg{Value: 4}), errRefused)
		synctest.Wait()

		// There's no rollback, so what a handler changed before refusing has to be kept.
		_, value := savedRoom(t, records, room)
		assert.Equal(t, int32(4), value)
	})
}

func TestInputThatCannotChangeTheGameIsNotSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := &writeCountingStore{Store: memory.New()}
		room, _ := newSavedRoom(t, records, RoomConfig{})
		synctest.Wait()
		before := records.count()

		sink := &fakeSink{}
		room.removeViewer("alice", sink)
		synctest.Wait()

		assert.Equal(t, before, records.count())
	})
}

func TestClosedRoomIsForgotten(t *testing.T) {
	tests := []struct {
		name  string
		close func(t *testing.T, room *Room)
		want  CloseReason
	}{
		{name: "on purpose", close: func(_ *testing.T, room *Room) { room.Close() }, want: CloseRequested},
		{
			name: "after a panic",
			close: func(t *testing.T, room *Room) {
				t.Helper()
				require.NoError(t, room.dispatch("alice", "saved", msgPanic, nil, nil))
			},
			want: ClosePanic,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := memory.New()
				room, _ := newSavedRoom(t, records, RoomConfig{})
				synctest.Wait()

				tt.close(t, room)
				waitFor(t, room.Done())
				synctest.Wait()

				assert.Equal(t, tt.want, room.Wait())
				requireNoRooms(t, records)
			})
		})
	}
}

func TestStuckRoomIsForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		records := memory.New()
		w := newTestWriter(t, records)
		rt := newConfiguredRuntime(t, Config{StuckTimeout: timeout})
		room, err := rt.createRoom(newTestRoomType(t, RoomConfig{Modules: []Module{savedModule}}), "code", w, nil)
		require.NoError(t, err)

		release := make(chan struct{})
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			<-release
			return nil
		}}))
		time.Sleep(timeout)
		synctest.Wait()
		assert.Equal(t, CloseStuck, room.Wait())
		requireNoRooms(t, records)

		// If the stuck goroutine wakes, it doesn't bring the room back.
		close(release)
		synctest.Wait()
		requireNoRooms(t, records)
	})
}

func TestShutdownSavesTheRoomsFinalState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		room, rt := newSavedRoom(t, records, RoomConfig{
			Close: func(rc *RoomContext, _ CloseReason) { savedKey.Get(rc).value = 9 },
		})

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		require.NoError(t, rt.Shutdown(ctx))
		synctest.Wait()

		_, value := savedRoom(t, records, room)
		assert.Equal(t, int32(9), value)
	})
}

func TestSlowStoreNeverHoldsUpARoomAndChangesCombine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const turns = 5
		counted := &writeCountingStore{Store: memory.New()}
		gated := &gatedStore{Store: counted, release: make(chan error)}
		room, _ := newSavedRoom(t, gated, RoomConfig{})
		synctest.Wait() // the writer is now stuck writing the first snapshot

		// With the store blocked, every turn still finishes.
		for range turns {
			require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: 1}))
		}

		gated.release <- nil
		gated.release <- nil
		synctest.Wait()

		_, value := savedRoom(t, counted, room)
		assert.Equal(t, int32(turns), value)
		assert.Equal(t, 2, counted.count(), "the first snapshot, then one write for all the turns")
	})
}

func TestStoreOutageRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		gated := &gatedStore{Store: records, release: make(chan error)}
		room, _ := newSavedRoom(t, gated, RoomConfig{})
		synctest.Wait()

		gated.release <- errStoreDown
		synctest.Wait()
		requireNoRooms(t, records)

		// While it's retrying, a newer snapshot replaces the one that failed.
		require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: 7}))

		time.Sleep(snapshotRetryDelay)
		synctest.Wait()
		gated.release <- nil
		synctest.Wait()

		_, value := savedRoom(t, records, room)
		assert.Equal(t, int32(7), value)
	})
}

func TestFailedWriteDoesNotReplaceANewerSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		gated := &gatedStore{Store: records, release: make(chan error)}
		w := newTestWriter(t, gated)
		record := func(data string) store.RoomRecord { return store.RoomRecord{ID: "room", Data: []byte(data)} }

		w.save(record("old"))
		synctest.Wait() // the writer is holding "old"
		w.save(record("new"))
		gated.release <- errStoreDown
		synctest.Wait()

		time.Sleep(snapshotRetryDelay)
		synctest.Wait()
		gated.release <- nil
		synctest.Wait()

		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		require.Len(t, rooms, 1)
		assert.Equal(t, "new", string(rooms[0].Data))
	})
}

func TestWriterFinishesWhatsPendingWhenClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		w := newSnapshotWriter(records, slog.New(slog.DiscardHandler))
		w.save(store.RoomRecord{ID: "room", Data: []byte("last")})

		w.close(context.Background())

		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		assert.Len(t, rooms, 1)
	})
}

func TestWriterGivesUpWhenClosingTakesTooLong(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newSnapshotWriter(downStore{}, slog.New(slog.DiscardHandler))
		w.save(store.RoomRecord{ID: "room"})
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		w.close(ctx)

		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
}

func TestSavedRandomNumbersContinueWhereTheyLeftOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		room, _ := newSavedRoom(t, records, RoomConfig{})

		// Move the generator along, and let the turn's snapshot save it.
		runInRoom(t, room, func(rc *RoomContext) { rc.Rand().Uint64() })
		synctest.Wait()
		snapshot, _ := savedRoom(t, records, room)

		restored := &rand.ChaCha8{}
		require.NoError(t, restored.UnmarshalBinary(snapshot.GetRngState()))

		var next uint64
		runInRoom(t, room, func(rc *RoomContext) { next = rc.Rand().Uint64() })
		assert.Equal(t, rand.New(restored).Uint64(), next) //nolint:gosec // only compared in a test
	})
}

func TestSnapshotRestoresTheModulesState(t *testing.T) {
	typ := newTestRoomType(t, RoomConfig{Modules: []Module{savedModule}})
	snapshot := &typ.snapshots[0]

	data, err := snapshot.encode(&counterState{value: 12})
	require.NoError(t, err)
	got, err := snapshot.decode(data)
	require.NoError(t, err)

	assert.Equal(t, &counterState{value: 12}, got)
}

func TestSnapshotWiringMistakes(t *testing.T) {
	save := func(_ *counterState) *wrapperspb.Int32Value { return nil }
	load := func(_ *wrapperspb.Int32Value) *counterState { return nil }
	newState := func() *counterState { return &counterState{} }
	stateKey := NewKey[counterState]("wiring")

	tests := []struct {
		name     string
		register func(r *Registry)
		want     string
	}{
		{
			name: "no save",
			register: func(r *Registry) {
				State(r, stateKey, newState)
				Snapshot[counterState, wrapperspb.Int32Value](r, stateKey, 1, nil, load)
			},
			want: "needs both",
		},
		{
			name: "no load",
			register: func(r *Registry) {
				State(r, stateKey, newState)
				Snapshot[counterState, wrapperspb.Int32Value](r, stateKey, 1, save, nil)
			},
			want: "needs both",
		},
		{
			name: "version zero",
			register: func(r *Registry) {
				State(r, stateKey, newState)
				Snapshot(r, stateKey, 0, save, load)
			},
			want: "versions start at 1",
		},
		{
			name: "two snapshots",
			register: func(r *Registry) {
				State(r, stateKey, newState)
				Snapshot(r, stateKey, 1, save, load)
				Snapshot(r, stateKey, 1, save, load)
			},
			want: "registered twice",
		},
		{
			name:     "state never registered",
			register: func(r *Registry) { Snapshot(r, stateKey, 1, save, load) },
			want:     "never registered",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRoomType(RoomConfig{Modules: []Module{registerFunc{name: "m", register: tt.register}}})

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestServerWithAStoreNeedsEveryStateSaved(t *testing.T) {
	tests := []struct {
		name    string
		modules []Module
		store   store.Store
		wantErr bool
	}{
		{name: "state with no snapshot", modules: []Module{counterModule{}}, store: memory.New(), wantErr: true},
		{name: "every state has one", modules: []Module{savedModule}, store: memory.New()},
		{name: "no state at all", modules: nil, store: memory.New()},
		{name: "no store, so nothing is saved", modules: []Module{counterModule{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewServer(newTestRuntime(t), &ServerConfig{
				RoomType:    newTestRoomType(t, RoomConfig{Modules: tt.modules}),
				Store:       tt.store,
				MemoryLimit: -1,
				Logger:      slog.New(slog.DiscardHandler),
			})

			if tt.wantErr {
				require.ErrorContains(t, err, "no snapshot")
				return
			}
			require.NoError(t, err)
			server.Close()
		})
	}
}

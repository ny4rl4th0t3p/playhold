package playhold

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const testCode2 = "room-2"

// restoreLobby is a lobby, runtime and writer on a store, like one server process.
func restoreLobby(t *testing.T, records store.Store, cfg RoomConfig, rtCfg Config) (*lobby, *Runtime) {
	t.Helper()
	w := newTestWriter(t, records) // first, so it closes after the runtime has shut down
	rt := newConfiguredRuntime(t, rtCfg)
	cfg.Modules = append(cfg.Modules, savedModule)
	return newLobby(rt, newTestRoomType(t, cfg), testIdle, testMaxRooms, w), rt
}

func stopProcess(t *testing.T, rt *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	require.NoError(t, rt.Shutdown(ctx))
	synctest.Wait()
}

func addTo(t *testing.T, room *Room, amount int32) {
	t.Helper()
	require.NoError(t, dispatchAndWait(t, room, "saved", msgAdd, &addMsg{Value: amount}))
}

func valueIn(t *testing.T, room *Room) int {
	t.Helper()
	var value int
	runInRoom(t, room, func(rc *RoomContext) { value = savedKey.Get(rc).value })
	return value
}

// unreadableStore is a Store that can't load saved rooms.
type unreadableStore struct{ *memory.Store }

func (unreadableStore) LoadRooms(_ context.Context) ([]store.RoomRecord, error) {
	return nil, errStoreDown
}

func TestServerWontStartIfItCannotReadSavedRooms(t *testing.T) {
	_, err := NewServer(newTestRuntime(t), &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{}),
		Store:       unreadableStore{memory.New()},
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
	})

	require.ErrorIs(t, err, errStoreDown)
}

func TestRoomComesBackAfterARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		inits, joins := 0, 0
		counting := registerFunc{name: "counting", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ PlayerJoined) { joins++ })
		}}
		cfg := RoomConfig{
			Modules: []Module{counting},
			Init: func(_ *RoomContext) error {
				inits++
				return nil
			},
		}

		firstLobby, firstRuntime := restoreLobby(t, records, cfg, Config{})
		first := enterRoom(t, firstLobby, testCode)
		require.NoError(t, seatAndWait(t, first, "alice"))
		addTo(t, first, 3)
		stopProcess(t, firstRuntime)

		secondLobby, _ := restoreLobby(t, records, cfg, Config{})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))
		second := enterRoom(t, secondLobby, testCode)

		assert.Equal(t, first.ID(), second.ID(), "it's the same room")
		assert.Equal(t, 3, valueIn(t, second))
		runInRoom(t, second, func(rc *RoomContext) { assert.Equal(t, []PlayerID{"alice"}, rc.Players()) })
		assert.Equal(t, 1, inits, "Init doesn't run again")
		assert.Equal(t, 1, joins, "a restored seat isn't a new player")

		// The seat is still there, so returning isn't a new join either.
		require.NoError(t, seatAndWait(t, second, "alice"))
		assert.Equal(t, 1, joins)
	})
}

func TestRestoredRoomContinuesTheSameRandomNumbers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		seed := func() [32]byte { return [32]byte{7} }
		draw := func(room *Room) uint64 {
			var got uint64
			runInRoom(t, room, func(rc *RoomContext) { got = rc.Rand().Uint64() })
			return got
		}

		// A room that's never interrupted shows what the second draw should be.
		uninterruptedLobby, _ := restoreLobby(t, memory.New(), RoomConfig{}, Config{NewSeed: seed})
		uninterrupted := enterRoom(t, uninterruptedLobby, testCode)
		firstDraw := draw(uninterrupted)
		secondDraw := draw(uninterrupted)

		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{NewSeed: seed})
		require.Equal(t, firstDraw, draw(enterRoom(t, firstLobby, testCode)))
		stopProcess(t, firstRuntime)

		// A different seed, so the numbers can only be right if they come from the snapshot.
		secondLobby, _ := restoreLobby(t, records, RoomConfig{}, Config{NewSeed: func() [32]byte { return [32]byte{9} }})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))

		assert.Equal(t, secondDraw, draw(enterRoom(t, secondLobby, testCode)))
	})
}

func TestRoomThatCannotBeRestoredDoesNotStopTheOthers(t *testing.T) {
	tests := []struct {
		name string
		bad  store.RoomRecord
	}{
		{name: "corrupt data", bad: store.RoomRecord{ID: "broken", Code: testCode2, RoomType: "", Data: []byte{0xff}}},
		{name: "another room type", bad: store.RoomRecord{ID: "other", Code: testCode2, RoomType: "chess"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := memory.New()
				firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
				first := enterRoom(t, firstLobby, testCode)
				synctest.Wait()
				stopProcess(t, firstRuntime)
				require.NoError(t, records.WriteRooms(t.Context(), []store.RoomRecord{tt.bad}, nil))

				secondLobby, _ := restoreLobby(t, records, RoomConfig{}, Config{})
				require.NoError(t, secondLobby.restoreAll(t.Context(), records))

				assert.Equal(t, first.ID(), enterRoom(t, secondLobby, testCode).ID())
				assert.NotEqual(t, RoomID(tt.bad.ID), enterRoom(t, secondLobby, testCode2).ID())

				// It stays stored, so a fixed server can try again.
				rooms, err := records.LoadRooms(t.Context())
				require.NoError(t, err)
				var ids []string
				for i := range rooms {
					ids = append(ids, rooms[i].ID)
				}
				assert.Contains(t, ids, tt.bad.ID)
			})
		})
	}
}

func TestRoomSavedAtAnotherVersionIsNotRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
		enterRoom(t, firstLobby, testCode)
		synctest.Wait()
		stopProcess(t, firstRuntime)

		// Pretend the module has moved on to a version with no migration.
		w := newTestWriter(t, records)
		rt := newTestRuntime(t)
		newer := registerFunc{name: "saved", register: func(r *Registry) {
			State(r, savedKey, func() *counterState { return &counterState{} })
			Snapshot(r, savedKey, savedVersion+1,
				func(s *counterState) *addMsg { return &addMsg{Value: int32(s.value)} }, //nolint:gosec // small numbers in a test
				func(msg *addMsg) *counterState { return &counterState{value: int(msg.GetValue())} },
			)
		}}
		l := newLobby(rt, newTestRoomType(t, RoomConfig{Modules: []Module{newer}}), testIdle, testMaxRooms, w)

		require.NoError(t, l.restoreAll(t.Context(), records))

		assert.Empty(t, l.rooms)
		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		assert.Len(t, rooms, 1, "the original stays, for a fixed server")
	})
}

func TestNewestRoomKeepsAReusedCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
		first := enterRoom(t, firstLobby, testCode)
		synctest.Wait()
		stopProcess(t, firstRuntime)

		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		older := rooms[0]
		older.ID = "00000000-0000-7000-8000-000000000000" // UUIDv7 sorts by time, so this is the oldest possible
		require.NoError(t, records.WriteRooms(t.Context(), []store.RoomRecord{older}, nil))

		secondLobby, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))
		synctest.Wait()

		assert.Equal(t, first.ID(), enterRoom(t, secondLobby, testCode).ID())
		rooms, err = records.LoadRooms(t.Context())
		require.NoError(t, err)
		require.Len(t, rooms, 1)
		assert.Equal(t, string(first.ID()), rooms[0].ID)
	})
}

func TestRestoredEmptyRoomStillClosesWhenAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
		enterRoom(t, firstLobby, testCode)
		synctest.Wait()
		stopProcess(t, firstRuntime)

		secondLobby, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))

		time.Sleep(testIdle)
		synctest.Wait()

		assert.Empty(t, secondLobby.rooms)
		requireNoRooms(t, records)
	})
}

func TestPanicRestoresTheRoomAndSkipsTheMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		closeHookRan := false
		l, _ := restoreLobby(t, records, RoomConfig{
			Close: func(_ *RoomContext, _ CloseReason) { closeHookRan = true },
		}, Config{})
		room := enterRoom(t, l, testCode)
		require.NoError(t, seatAndWait(t, room, "alice"))
		addTo(t, room, 3)

		results := make(chan error, 1)
		require.NoError(t, room.dispatch("alice", "saved", msgPanic, nil, func(err error) { results <- err }))
		require.ErrorIs(t, <-results, ErrMessageFailed)
		waitFor(t, room.Done())
		synctest.Wait()

		next := enterRoom(t, l, testCode)
		assert.NotSame(t, room, next)
		assert.Equal(t, room.ID(), next.ID())
		assert.Equal(t, 3, valueIn(t, next))
		assert.False(t, closeHookRan, "the room isn't over")

		_, value := savedRoom(t, records, next)
		assert.Equal(t, int32(3), value, "the snapshot wasn't forgotten")

		addTo(t, next, 1)
		assert.Equal(t, 4, valueIn(t, next))
	})
}

func TestStuckRoomIsRestoredAndItsOldGoroutineCannotTouchTheCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		records := memory.New()
		l, _ := restoreLobby(t, records, RoomConfig{}, Config{StuckTimeout: timeout})
		room := enterRoom(t, l, testCode)
		addTo(t, room, 3)

		release := make(chan struct{})
		require.NoError(t, room.submit(input{run: func(rc *RoomContext) error {
			state := savedKey.Get(rc)
			<-release
			state.value = 100 // what a stuck handler might do when it finally wakes
			savedKey.Get(rc).value = 200
			return nil
		}}))
		time.Sleep(timeout)
		synctest.Wait()
		assert.True(t, room.closed())

		next := enterRoom(t, l, testCode)
		assert.Equal(t, 3, valueIn(t, next))

		close(release)
		synctest.Wait()

		assert.Equal(t, 3, valueIn(t, next))
		_, value := savedRoom(t, records, next)
		assert.Equal(t, int32(3), value)
	})
}

func TestRoomThatKeepsCrashingIsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		l, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		room := enterRoom(t, l, testCode)
		synctest.Wait()

		for range maxRestarts {
			crash(t, room)
			next := l.replacement(testCode, room)
			require.NotNil(t, next, "it restarts up to the limit")
			room = next
		}

		crash(t, room)

		assert.Nil(t, l.replacement(testCode, room), "it closes after the limit")
		assert.Empty(t, l.rooms)
		requireNoRooms(t, records)
	})
}

func TestRestartLimitOnlyCountsRecentCrashes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		l, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		room := enterRoom(t, l, testCode)
		synctest.Wait()

		for range 2 * maxRestarts {
			crash(t, room)
			room = l.replacement(testCode, room)
			require.NotNil(t, room, "crashes this far apart never hit the limit")
			time.Sleep(restartWindow)
		}
	})
}

func TestConnectionsStayCountedAcrossARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		l, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		room := enterRoom(t, l, testCode)
		synctest.Wait()

		crash(t, room)
		next := l.replacement(testCode, room)
		require.NotNil(t, next)

		// The connection's own leave, still holding the old room, has to count.
		time.Sleep(2 * testIdle)
		synctest.Wait()
		assert.False(t, next.closed(), "still counted as connected")

		l.leave(testCode, room)
		time.Sleep(testIdle)
		synctest.Wait()
		assert.True(t, next.closed())
	})
}

func TestCrashWithoutAStoreJustClosesTheRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLobby(newTestRuntime(t), newTestRoomType(t, RoomConfig{Modules: []Module{savedModule}}), testIdle, testMaxRooms, nil)
		room := enterRoom(t, l, testCode)

		crash(t, room)

		assert.Equal(t, ClosePanic, room.Wait())
		assert.Nil(t, l.replacement(testCode, room))
	})
}

// crash panics the room, and waits for whatever happens next.
func crash(t *testing.T, room *Room) {
	t.Helper()
	require.NoError(t, room.dispatch("alice", "saved", msgPanic, nil, nil))
	waitFor(t, room.Done())
	synctest.Wait()
}

func TestApplySnapshotReportsAPanickingLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
		enterRoom(t, firstLobby, testCode)
		synctest.Wait()
		stopProcess(t, firstRuntime)

		panicking := registerFunc{name: "saved", register: func(r *Registry) {
			State(r, savedKey, func() *counterState { return &counterState{} })
			Snapshot(r, savedKey, savedVersion,
				func(s *counterState) *addMsg { return &addMsg{Value: int32(s.value)} }, //nolint:gosec // small numbers in a test
				func(_ *addMsg) *counterState { panic("boom") },
			)
		}}
		w := newTestWriter(t, records)
		l := newLobby(newTestRuntime(t), newTestRoomType(t, RoomConfig{Modules: []Module{panicking}}), testIdle, testMaxRooms, w)

		require.NoError(t, l.restoreAll(t.Context(), records))

		assert.Empty(t, l.rooms)
	})
}

func TestPlayerKeepsPlayingThroughACrash(t *testing.T) {
	url := startServerWith(t, &ServerConfig{
		RoomType: newTestRoomType(t, RoomConfig{Modules: []Module{viewOfSaved, savedModule}}),
		Store:    memory.New(),
	})
	ws, _ := seatedGuest(t, url)
	assert.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	require.Equal(t, "5", savedView(t, readServerFrame(t, ws)))

	sendFrame(t, ws, messageFrame(t, 4, "saved", msgPanic, &addMsg{}))

	// The sender is told that move failed, and gets a full view of the restored room, with no RoomClosed.
	failure := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_REJECTED, failure.GetCode())
	assert.Equal(t, uint64(4), failure.GetSeq())
	require.Equal(t, "5", savedView(t, readServerFrame(t, ws)))

	sendFrame(t, ws, messageFrame(t, 5, "saved", msgAdd, &addMsg{Value: 1}))
	assert.Equal(t, "6", savedView(t, readServerFrame(t, ws)))
}

func TestPlayerIsToldWhenARoomCrashesTooOften(t *testing.T) {
	url := startServerWith(t, &ServerConfig{
		RoomType: newTestRoomType(t, RoomConfig{Modules: []Module{viewOfSaved, savedModule}}),
		Store:    memory.New(),
	})
	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	// The view after each restart shows this connection has moved to the new room, so the next crash reaches it.
	for seq := range uint64(maxRestarts) {
		sendFrame(t, ws, messageFrame(t, seq+3, "saved", msgPanic, &addMsg{}))
		require.NotNil(t, readServerFrame(t, ws).GetError())
		require.NotNil(t, readServerFrame(t, ws).GetState())
	}

	// This crash is the one over the limit.
	sendFrame(t, ws, messageFrame(t, 10, "saved", msgPanic, &addMsg{}))

	require.NotNil(t, readServerFrame(t, ws).GetError())
	assert.Equal(t, wire.CloseReason_CLOSE_REASON_PANIC, readServerFrame(t, ws).GetClosed().GetReason())
}

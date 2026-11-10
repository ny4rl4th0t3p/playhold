package playhold

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func testBreaker(t *testing.T, records store.Store, build string, crashed []JournalEntry) *breaker {
	t.Helper()
	b, err := newBreaker(t.Context(), records, build, slog.New(slog.DiscardHandler), crashed)
	require.NoError(t, err)
	t.Cleanup(b.wait)
	return b
}

var deckMove = handlerKey{module: "deck", id: 7}

func TestBreakerTurnsAMessageOffForAnHourAtItsThirdIncidentInTheWindow(t *testing.T) {
	tests := []struct {
		name  string
		after []time.Duration // since the first incident
		event bool            // a timer's incidents, which no breaker can refuse
		want  bool
	}{
		{name: "three within the window", after: []time.Duration{0, 4 * time.Minute, 10 * time.Minute}, want: true},
		{name: "three spread wider than the window", after: []time.Duration{0, 6 * time.Minute, 12 * time.Minute}},
		{name: "two", after: []time.Duration{0, time.Minute}},
		{name: "a timer's, however many", after: []time.Duration{0, 0, 0, 0}, event: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBreaker(t, memory.New(), "build", nil)
			start := time.Now()
			var last time.Time
			for _, after := range tt.after {
				last = start.Add(after)
				b.now = func() time.Time { return last }
				incident := store.Incident{Kind: "crashed", Room: "r", Module: deckMove.module, MessageID: deckMove.id}
				if tt.event {
					incident = store.Incident{Kind: "crashed", Room: "r", Event: "game.Tick"}
				}
				b.record(&incident)
			}

			assert.Equal(t, tt.want, b.turnedOff(deckMove))
			assert.False(t, b.turnedOff(handlerKey{module: "deck", id: 8}), "only that message")
			assert.False(t, b.turnedOff(handlerKey{}))

			b.now = func() time.Time { return last.Add(turnedOffFor) }
			assert.False(t, b.turnedOff(deckMove), "tried again an hour after the last incident")
		})
	}
}

func TestATurnedOffMessageStaysOffAfterARestartButNotWithANewBuild(t *testing.T) {
	records := memory.New()
	first := testBreaker(t, records, "build-1", nil)
	for range breakerIncidents {
		first.record(&store.Incident{Kind: "crashed", Room: "r", Module: deckMove.module, MessageID: deckMove.id})
	}
	first.wait()

	assert.True(t, testBreaker(t, records, "build-1", nil).turnedOff(deckMove), "the same build")
	assert.False(t, testBreaker(t, records, "build-2", nil).turnedOff(deckMove), "a new build")
	incidents, err := records.LoadIncidents(t.Context())
	require.NoError(t, err)
	assert.Empty(t, incidents, "the old build's are deleted")
}

func TestAMessageIsTriedAgainAfterARestartAnHourAfterItsLastIncident(t *testing.T) {
	records := memory.New()
	at := time.Now().Add(-turnedOffFor - time.Minute)
	for range breakerIncidents {
		incident := store.Incident{Build: "build", Kind: "crashed", Module: deckMove.module, MessageID: deckMove.id, At: at}
		require.NoError(t, records.SaveIncident(t.Context(), &incident))
	}

	assert.False(t, testBreaker(t, records, "build", nil).turnedOff(deckMove))
}

func TestIncidentsOlderThanTheHistoryAreDeleted(t *testing.T) {
	records := memory.New()
	old := time.Now().Add(-incidentHistory - time.Hour)
	incident := store.Incident{Build: "build", Kind: "crashed", Module: deckMove.module, MessageID: deckMove.id, At: old}
	require.NoError(t, records.SaveIncident(t.Context(), &incident))

	testBreaker(t, records, "build", nil)

	incidents, err := records.LoadIncidents(t.Context())
	require.NoError(t, err)
	assert.Empty(t, incidents)
}

func TestARoomThatKeepsCrashingTheServerIsFound(t *testing.T) {
	tests := []struct {
		name string
		ago  []time.Duration
		want bool
	}{
		{name: "two in the window", ago: []time.Duration{time.Minute, 9 * time.Minute}, want: true},
		{name: "one", ago: []time.Duration{time.Minute}},
		{name: "two, one of them older than the window", ago: []time.Duration{time.Minute, 11 * time.Minute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := memory.New()
			for _, ago := range tt.ago {
				incident := store.Incident{Build: "build", Kind: "crashed", Room: "r", Event: "game.Tick", At: time.Now().Add(-ago)}
				require.NoError(t, records.SaveIncident(t.Context(), &incident))
			}

			b := testBreaker(t, records, "build", nil)

			assert.Equal(t, tt.want, b.keepsCrashing("r"))
			assert.False(t, b.keepsCrashing("another"))
		})
	}
}

func TestARoomThatKeepsCrashingTheServerIsNotRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		firstLobby, firstRuntime := restoreLobby(t, records, RoomConfig{}, Config{})
		first := enterRoom(t, firstLobby, testCode)
		synctest.Wait()
		stopProcess(t, firstRuntime)
		for range roomIncidents {
			incident := store.Incident{Build: "build", Kind: "crashed", Room: string(first.ID()), Event: "game.Tick", At: time.Now()}
			require.NoError(t, records.SaveIncident(t.Context(), &incident))
		}

		secondLobby, _ := restoreLobby(t, records, RoomConfig{}, Config{})
		secondLobby.breaker = testBreaker(t, records, "build", nil)
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))
		assert.True(t, secondLobby.savedCodes[testCode], "a room left in the store keeps its code from new rooms")

		assert.NotEqual(t, first.ID(), enterRoom(t, secondLobby, testCode).ID(), "the code gets a new room")
		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		var ids []string
		for i := range rooms {
			ids = append(ids, rooms[i].ID)
		}
		assert.Contains(t, ids, string(first.ID()), "it stays stored, for a fixed server to try again")
	})
}

func TestAMessageThatKeepsCrashingTheProcessIsTurnedOff(t *testing.T) {
	cfg := JournalConfig{Path: filepath.Join(t.TempDir(), "journal"), Entries: 8, EntrySize: 512}
	records := memory.New()

	// Each start reads the journal the last one left, then dies while the same message runs.
	for start := range breakerIncidents + 1 {
		j := testJournal(t, cfg, nil)
		b := testBreaker(t, records, "build", j.crashed)

		assert.Equal(t, start == breakerIncidents, b.turnedOff(deckMove), "start %d", start)
		assert.Equal(t, start >= roomIncidents, b.keepsCrashing("r"), "start %d", start)
		write(j, &JournalEntry{Room: "r", Player: "mallory", Module: deckMove.module, MessageID: deckMove.id})
		require.NoError(t, j.close())
	}
}

func TestOnlyATurnStillRunningLongAfterTheWatchdogIsAnIncident(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		module := registerFunc{name: "m", register: func(r *Registry) {
			Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error { panic("boom") })
			Handle(r, msgBlock, func(_ *RoomContext, _ *addMsg) error {
				<-release
				return nil
			})
		}}
		records := memory.New()
		b := testBreaker(t, records, "build", nil)
		rt := newConfiguredRuntime(t, Config{StuckTimeout: timeout})
		rt.useBreaker(b)
		watch := newStuckWatch(slog.New(slog.DiscardHandler), nil, false, func() {})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rt.watchStuck(func(room *Room) { go watch.follow(ctx, room) })
		typ := newTestRoomType(t, RoomConfig{Modules: []Module{module}})
		incidents := func() []store.Incident {
			b.wait()
			got, err := records.LoadIncidents(t.Context())
			require.NoError(t, err)
			return got
		}

		crashed, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		require.NoError(t, crashed.dispatch("bob", "m", msgFail, mustMarshal(t, &addMsg{}), nil))
		waitFor(t, crashed.Done())
		assert.Empty(t, incidents(), "a panic only hurts its own room")

		stuck, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		require.NoError(t, stuck.dispatch("carol", "m", msgBlock, mustMarshal(t, &addMsg{}), nil))
		time.Sleep(timeout)
		synctest.Wait()
		assert.Empty(t, incidents(), "stuck, but it may still end by itself")

		time.Sleep(stuckGrace)
		synctest.Wait()
		got := incidents()
		require.Len(t, got, 1)
		assert.Equal(t, store.Incident{
			Build: "build", Kind: "stuck", Room: string(stuck.ID()), Player: "carol", Module: "m", MessageID: msgBlock,
			At: got[0].At,
		}, got[0])

		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
	})
}

func TestAPlayerIsToldAMessageIsTurnedOff(t *testing.T) {
	build, err := currentBuild()
	require.NoError(t, err)
	records := memory.New()
	for range breakerIncidents {
		incident := store.Incident{Build: build, Kind: "crashed", Module: "saved", MessageID: msgPanic, At: time.Now()}
		require.NoError(t, records.SaveIncident(t.Context(), &incident))
	}
	url := startServerWith(t, &ServerConfig{
		RoomType: newTestRoomType(t, RoomConfig{Modules: []Module{viewOfSaved, savedModule}}),
		Store:    records,
	})
	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgPanic, &addMsg{}))

	refused := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_TURNED_OFF, refused.GetCode())
	assert.Equal(t, uint64(3), refused.GetSeq())
	sendFrame(t, ws, messageFrame(t, 4, "saved", msgAdd, &addMsg{Value: 1}))
	assert.NotNil(t, readServerFrame(t, ws).GetState(), "other messages still work, in the same room")
}

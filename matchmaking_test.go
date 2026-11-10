package playhold

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// These use a real network connection, so real time; the matcher runs often, so it doesn't slow them down.
const testMatchEvery = 10 * time.Millisecond

func queueFrame(seq uint64, properties map[string]string) *wire.ClientFrame {
	return &wire.ClientFrame{Seq: seq, Body: &wire.ClientFrame_Queue{Queue: &wire.Queue{Properties: properties}}}
}

// startMatchServer returns a server that matches players, and its URL.
func startMatchServer(t *testing.T, rooms RoomConfig, matching *Matchmaking) (server *Server, url string) {
	t.Helper()
	if matching.Every == 0 {
		matching.Every = testMatchEvery
	}
	server, err := NewServer(newTestRuntime(t), &ServerConfig{
		RoomType:    newTestRoomType(t, rooms),
		Matchmaking: matching,
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
		Development: true,
		Admission:   Admission{Off: true},
	})
	require.NoError(t, err)
	return server, startServerFor(t, server)
}

// queued connects a new guest and puts them in the queue, with no properties.
func queued(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ws, _ := newGuest(t, url)
	sendFrame(t, ws, queueFrame(3, nil))
	require.Equal(t, uint64(3), readServerFrame(t, ws).GetQueued().GetSeq())
	return ws
}

func matchedCode(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	matched := readServerFrame(t, ws).GetMatched()
	require.NotNil(t, matched)
	return matched.GetCode()
}

func TestTwoPlayersAreMatchedIntoOneRoomWhoseSeatsWaitForThem(t *testing.T) {
	server, url := startMatchServer(t, RoomConfig{MaxPlayers: 3}, &Matchmaking{Matcher: groupsInOrder(2)})
	alice := queued(t, url)
	bob := queued(t, url)

	code := matchedCode(t, alice)
	assert.Equal(t, code, matchedCode(t, bob), "one room for the group")
	assert.Len(t, code, 26, "a code the server made")
	assert.Zero(t, server.lobby.matchmaker.waitingCount())
	assert.InDelta(t, 1.0, testutil.ToFloat64(server.metrics.matches), 0)

	carol, _ := newGuest(t, url)
	sendFrame(t, carol, joinFrame(code))
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_FULL, readServerFrame(t, carol).GetError().GetCode(),
		"the free seats wait for the matched players")

	joinRoom(t, alice, code)
	joinRoom(t, bob, code)
	dave, _ := newGuest(t, url)
	joinRoom(t, dave, code) // the match is complete, so the room is open like any other
}

func TestAMatchedPlayerWhoDoesntComeClosesTheRoom(t *testing.T) {
	_, url := startMatchServer(t, RoomConfig{MaxPlayers: 2}, &Matchmaking{ReserveFor: 200 * time.Millisecond})
	alice := queued(t, url)
	bob := queued(t, url)
	code := matchedCode(t, alice)
	matchedCode(t, bob) // bob never joins

	joinRoom(t, alice, code)

	assert.Equal(t, wire.CloseReason_CLOSE_REASON_MATCH_INCOMPLETE, readServerFrame(t, alice).GetClosed().GetReason())
}

func TestTheMatcherSeesWhatDescribeAndTheClientSaid(t *testing.T) {
	var levelsMu sync.Mutex // Describe reads it on the connection's goroutine
	levels := map[PlayerID]int{}
	seen := make(chan []Candidate, 1)
	_, url := startMatchServer(t, RoomConfig{MaxPlayers: 2}, &Matchmaking{
		Describe: func(_ context.Context, player PlayerID) (any, error) {
			levelsMu.Lock()
			defer levelsMu.Unlock()
			return levels[player], nil
		},
		Matcher: func(waiting []Candidate) [][]PlayerID {
			if len(waiting) == 2 {
				select {
				case seen <- waiting:
				default:
				}
			}
			return nil // nobody is matched, so the test can look
		},
	})
	ws, welcome := newGuest(t, url)
	levelsMu.Lock()
	levels[PlayerID(welcome.GetPlayerId())] = 12
	levelsMu.Unlock()
	sendFrame(t, ws, queueFrame(3, map[string]string{"mode": "ranked"}))
	require.NotNil(t, readServerFrame(t, ws).GetQueued())
	queued(t, url)

	waiting := <-seen
	assert.Equal(t, PlayerID(welcome.GetPlayerId()), waiting[0].Player, "in the order they queued")
	assert.Equal(t, 12, waiting[0].Info)
	assert.Equal(t, map[string]string{"mode": "ranked"}, waiting[0].Properties)
	assert.Equal(t, 0, waiting[1].Info)
	assert.Positive(t, waiting[0].Waiting)
}

func TestLeavingOrDisconnectingLeavesTheQueue(t *testing.T) {
	server, url := startMatchServer(t, RoomConfig{MaxPlayers: 3}, &Matchmaking{})
	alice := queued(t, url)
	bob := queued(t, url)
	require.Equal(t, 2, server.lobby.matchmaker.waitingCount())

	sendFrame(t, alice, &wire.ClientFrame{Seq: 4, Body: &wire.ClientFrame_LeaveQueue{LeaveQueue: &wire.LeaveQueue{}}})
	require.Eventually(t, func() bool { return server.lobby.matchmaker.waitingCount() == 1 }, testTimeout, testMatchEvery)

	_ = bob.CloseNow()
	require.Eventually(t, func() bool { return server.lobby.matchmaker.waitingCount() == 0 }, testTimeout, testMatchEvery)
}

func TestQueueRefusals(t *testing.T) {
	tooMany := map[string]string{}
	for i := range maxQueueProperties + 1 {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	tests := []struct {
		name     string
		matching *Matchmaking
		hello    bool
		props    map[string]string
		want     wire.ErrorCode
	}{
		{name: "before Hello", matching: &Matchmaking{}, want: wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME},
		{name: "no matchmaking on this server", hello: true, want: wire.ErrorCode_ERROR_CODE_REJECTED},
		{name: "too many properties", matching: &Matchmaking{}, hello: true, props: tooMany,
			want: wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME},
		{name: "a property too long", matching: &Matchmaking{}, hello: true,
			props: map[string]string{"mode": strings.Repeat("x", maxQueuePropertyLen+1)}, want: wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME},
		{name: "the game can't describe the player", hello: true,
			matching: &Matchmaking{Describe: func(context.Context, PlayerID) (any, error) { return nil, errors.New("db down") }},
			want:     wire.ErrorCode_ERROR_CODE_UNAVAILABLE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var url string
			if tt.matching == nil {
				url = startGameServer(t, RoomConfig{MaxPlayers: 2}, 0)
			} else {
				_, url = startMatchServer(t, RoomConfig{MaxPlayers: 2}, tt.matching)
			}
			ws := dialServer(t, url)
			if tt.hello {
				ws, _ = newGuest(t, url)
			}

			sendFrame(t, ws, queueFrame(3, tt.props))

			assert.Equal(t, tt.want, readServerFrame(t, ws).GetError().GetCode())
		})
	}
}

func TestABadMatcherMatchesNobody(t *testing.T) {
	tests := []struct {
		name    string
		matcher Matcher
	}{
		{name: "a player who isn't waiting", matcher: func(_ []Candidate) [][]PlayerID { return [][]PlayerID{{"nobody"}} }},
		{name: "a player twice", matcher: func(waiting []Candidate) [][]PlayerID {
			return [][]PlayerID{{waiting[0].Player}, {waiting[0].Player}}
		}},
		{name: "a group too big", matcher: func(waiting []Candidate) [][]PlayerID {
			return [][]PlayerID{{waiting[0].Player, waiting[1].Player, "third"}}
		}},
		{name: "a panic", matcher: func(_ []Candidate) [][]PlayerID { panic("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, url := startMatchServer(t, RoomConfig{MaxPlayers: 2}, &Matchmaking{Matcher: tt.matcher})
			queued(t, url)
			queued(t, url)

			time.Sleep(5 * testMatchEvery) // several runs of the matcher

			assert.Equal(t, 2, server.lobby.matchmaker.waitingCount(), "both still waiting")
			assert.Zero(t, testutil.ToFloat64(server.metrics.matches))
		})
	}
}

func TestCheckGroups(t *testing.T) {
	waiting := []Candidate{{Player: "a"}, {Player: "b"}, {Player: "c"}}
	tests := []struct {
		name    string
		groups  [][]PlayerID
		wantErr bool
	}{
		{name: "groups of waiting players", groups: [][]PlayerID{{"a", "b"}, {"c"}}},
		{name: "nobody", groups: nil},
		{name: "a player who isn't waiting", groups: [][]PlayerID{{"a", "z"}}, wantErr: true},
		{name: "a player in two groups", groups: [][]PlayerID{{"a"}, {"a", "b"}}, wantErr: true},
		{name: "a player twice in one group", groups: [][]PlayerID{{"a", "a"}}, wantErr: true},
		{name: "more than the room's seats", groups: [][]PlayerID{{"a", "b", "c"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGroups(tt.groups, waiting, 2)

			if tt.wantErr {
				require.ErrorIs(t, err, errBadGroups)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTheDefaultMatcherFillsGroupsInOrder(t *testing.T) {
	waiting := []Candidate{{Player: "a"}, {Player: "b"}, {Player: "c"}, {Player: "d"}, {Player: "e"}}

	groups := groupsInOrder(2)(waiting)

	assert.Equal(t, [][]PlayerID{{"a", "b"}, {"c", "d"}}, groups, "e waits for someone")
}

func TestTheDefaultMatcherNeedsAGroupSize(t *testing.T) {
	_, err := NewServer(newTestRuntime(t), &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{}),
		Matchmaking: &Matchmaking{},
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
	})

	require.ErrorContains(t, err, "MaxPlayers")
}

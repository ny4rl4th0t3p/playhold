package cards_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/playholdtest"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// These tests run the game on a real server, for what only a server does: the handshake and its schema check, refusing a
// seat over the protocol, and what reaches the store. The game's rules are tested without a server, in cards_test.go.

const (
	testTimeout = 5 * time.Second
	testRoom    = "game"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// startServer returns the URL of a server for the game, saving to records if it isn't nil.
func startServer(t *testing.T, records store.Store) string {
	t.Helper()
	url, _ := startServerWith(t, records, "")
	return url
}

// startServerWith is startServer, recording every game in recordings if it isn't empty. It also returns a function that
// stops the server, which writes everything waiting to be written.
func startServerWith(t *testing.T, records store.Store, recordings string) (url string, stop func()) {
	t.Helper()
	rt := playhold.NewRuntime(playhold.Config{
		Logger:  slog.New(slog.DiscardHandler),
		NewSeed: func() [32]byte { return testSeed },
	})
	typ, err := cards.NewRoomTypeWith(cards.Options{Record: recordings != ""})
	require.NoError(t, err)
	off := playhold.RateLimit{Off: true}
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{
		RoomType:         typ,
		Store:            records,
		Recording:        playhold.RecordingConfig{Dir: recordings},
		MemoryLimit:      -1,
		Development:      true, // plain HTTP
		AllowClientCodes: true, // the tests pick their rooms' codes
		ConnectRate:      off,
		GuestRate:        off,
		MessageRate:      off,
		Admission:        playhold.Admission{Off: true},
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	var once sync.Once
	stop = func() {
		once.Do(func() {
			server.Close()
			httpServer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			assert.NoError(t, rt.Shutdown(ctx))
		})
	}
	t.Cleanup(stop)
	return "ws" + strings.TrimPrefix(httpServer.URL, "http"), stop
}

// client is a test client on a WebSocket.
type client struct {
	t   *testing.T
	ws  *websocket.Conn
	id  string
	seq uint64
}

// dial connects without saying hello.
func dial(t *testing.T, url string) *client {
	t.Helper()
	ws, _, err := websocket.Dial(testContext(t), url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &client{t: t, ws: ws}
}

// connect says hello as a new guest.
func connect(t *testing.T, url string) *client {
	t.Helper()
	c := dial(t, url)
	c.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: 1, SchemaVersions: cards.ClientSchemaVersions()},
	}})
	welcome := c.read().GetWelcome()
	require.NotNil(t, welcome)
	c.id = welcome.GetPlayerId()
	return c
}

func (c *client) sendFrame(frame *wire.ClientFrame) {
	c.t.Helper()
	c.seq++
	frame.Seq = c.seq
	data, err := proto.Marshal(frame)
	require.NoError(c.t, err)
	require.NoError(c.t, c.ws.Write(testContext(c.t), websocket.MessageBinary, data))
}

func (c *client) read() *wire.ServerFrame {
	c.t.Helper()
	_, data, err := c.ws.Read(testContext(c.t))
	require.NoError(c.t, err)
	frame := &wire.ServerFrame{}
	require.NoError(c.t, proto.Unmarshal(data, frame))
	return frame
}

// move sends one of the hands module's messages, and reads the state its turn sends back.
func (c *client) move(id pb.HandsMessage, msg proto.Message) {
	c.t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(c.t, err)
	c.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: "hands", Id: uint32(id), Payload: payload}, //nolint:gosec // a small enum
	}})
	require.NotNil(c.t, c.read().GetState())
}

// join joins the test room, and reads the state that follows.
func (c *client) join() {
	c.t.Helper()
	c.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: testRoom}}})
	require.NotNil(c.t, c.read().GetJoined())
	require.NotNil(c.t, c.read().GetState())
}

// hand returns the player's own cards from a state.
func hand(t *testing.T, state *wire.RoomState) []string {
	t.Helper()
	for _, view := range state.GetViews() {
		if view.GetModule() == "hands" {
			got := &pb.HandsView{}
			require.NoError(t, proto.Unmarshal(view.GetView(), got))
			return got.GetCards()
		}
	}
	return nil
}

// startServerGame seats two new guests, which starts the game, and returns the first, with the hand they were dealt.
func startServerGame(t *testing.T, url string) (first *client, firstHand []string) {
	t.Helper()
	first = connect(t, url)
	first.join()
	connect(t, url).join()
	state := first.read().GetState() // the game has started
	require.NotNil(t, state)
	return first, hand(t, state)
}

func TestGameIsSavedWithEverythingItNeeds(t *testing.T) {
	records := memory.New()
	first, dealt := startServerGame(t, startServer(t, records))
	require.NotEmpty(t, dealt)

	var modules []*wire.ModuleSnapshot
	require.Eventually(t, func() bool {
		rooms, err := records.LoadRooms(testContext(t))
		if err != nil || len(rooms) != 1 {
			return false
		}
		snapshot := &wire.RoomSnapshot{}
		if proto.Unmarshal(rooms[0].Data, snapshot) != nil {
			return false
		}
		state := &pb.HandsState{}
		for _, module := range snapshot.GetModules() {
			if module.GetModule() == "hands" && proto.Unmarshal(module.GetState(), state) != nil {
				return false
			}
		}
		modules = snapshot.GetModules()
		return slices.Equal(state.GetHands()[first.id].GetCards(), dealt)
	}, testTimeout, 10*time.Millisecond, "the saved hand never caught up with the game")

	names := make([]string, 0, len(modules))
	for _, module := range modules {
		names = append(names, module.GetModule())
		assert.Equal(t, uint32(1), module.GetVersion())
	}
	assert.Equal(t, []string{"deck", "turns", "hands"}, names)
}

func TestARecordedGameReplaysTurnForTurn(t *testing.T) {
	recordings := t.TempDir()
	url, stop := startServerWith(t, nil, recordings)
	first, _ := startServerGame(t, url)
	first.move(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}) // whoever arrived first goes first
	first.move(pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{})
	stop()

	files, err := filepath.Glob(filepath.Join(recordings, "crazy-eights", "*.done.rec"))
	require.NoError(t, err)
	require.Len(t, files, 1, "one room, in the room type's folder, finished: nothing saves it to be restored")
	// #region replay
	typ, err := cards.NewRoomTypeWith(cards.Options{Record: true})
	require.NoError(t, err)

	report := playholdtest.Replay(t, typ, files[0]) // fails the test at the first turn that differs

	assert.Equal(t, 4, report.Turns, "two seats, a draw and a pass")
	assert.True(t, report.SchemaMatches)
	// #endregion replay
}

func TestPageFromBeforeSchemasIsToldToUpdate(t *testing.T) {
	c := dial(t, startServer(t, nil))

	c.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Hello{Hello: &wire.Hello{ProtocolVersion: 1}}})

	assert.Equal(t, wire.ErrorCode_ERROR_CODE_UPDATE_REQUIRED, c.read().GetError().GetCode())
}

func TestThirdPlayerIsRefused(t *testing.T) {
	url := startServer(t, nil)
	startServerGame(t, url)

	third := connect(t, url)
	third.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: testRoom}}})

	assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_FULL, third.read().GetError().GetCode())
}

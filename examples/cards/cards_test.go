package cards_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/hands"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const (
	testTimeout   = 5 * time.Second
	testRoom      = "game"
	maxMoves      = 5000
	totalCards    = 52
	reconnectTurn = 5
)

// testSeed is recognizable in raw bytes, so the hidden-card test can look for it.
var testSeed = [32]byte([]byte("playhold-test-seed-do-not-leak!!"))

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// snapshot is one player's latest view of the game.
type snapshot struct {
	deck  *pb.DeckView
	turns *pb.TurnsView
	hand  *pb.HandsView
}

// seat is a test client. It keeps every raw frame it receives, and checks each one for hidden information.
type seat struct {
	t      *testing.T
	ws     *websocket.Conn
	id     string
	secret string
	seq    uint64
	latest snapshot
	frames [][]byte
}

// game is a two-player game on a real server.
type game struct {
	t     *testing.T
	url   string
	first *seat
	other *seat
}

func startServer(t *testing.T, seed [32]byte) string {
	t.Helper()
	url, _ := startServerWith(t, seed, nil)
	return url
}

// startServerWith returns the server's URL, and a function that stops it, as a deploy would.
func startServerWith(t *testing.T, seed [32]byte, records store.Store) (url string, stop func()) {
	t.Helper()
	rt := playhold.NewRuntime(playhold.Config{
		Logger:  slog.New(slog.DiscardHandler),
		NewSeed: func() [32]byte { return seed },
	})
	typ, err := cards.NewRoomType()
	require.NoError(t, err)
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{RoomType: typ, Store: records, MemoryLimit: -1})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	stop = func() {
		server.Close()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		assert.NoError(t, rt.Shutdown(ctx))
	}
	t.Cleanup(stop)
	return "ws" + strings.TrimPrefix(httpServer.URL, "http"), stop
}

func startGame(t *testing.T, seed [32]byte) *game {
	t.Helper()
	return startGameOn(t, startServer(t, seed))
}

func startGameOn(t *testing.T, url string) *game {
	t.Helper()
	first := connect(t, url, "", "")
	second := connect(t, url, "", "")

	first.join()
	first.readState() // alone in the room
	second.join()
	second.readState() // the game has started
	first.readState()

	return &game{t: t, url: url, first: first, other: second}
}

// connect says hello, as a new guest if the ID and secret are empty.
func connect(t *testing.T, url, id, secret string) *seat {
	t.Helper()
	ws, _, err := websocket.Dial(testContext(t), url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.CloseNow() })

	s := &seat{t: t, ws: ws, id: id, secret: secret}
	s.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: 1, PlayerId: id, Secret: secret},
	}})
	welcome := s.read().GetWelcome()
	require.NotNil(t, welcome)
	s.id = welcome.GetPlayerId()
	if welcome.GetSecret() != "" {
		s.secret = welcome.GetSecret()
	}
	return s
}

func (s *seat) sendFrame(frame *wire.ClientFrame) {
	s.t.Helper()
	s.seq++
	frame.Seq = s.seq
	data, err := proto.Marshal(frame)
	require.NoError(s.t, err)
	require.NoError(s.t, s.ws.Write(testContext(s.t), websocket.MessageBinary, data))
}

func (s *seat) join() {
	s.t.Helper()
	s.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: testRoom}}})
	require.NotNil(s.t, s.read().GetJoined())
}

func (s *seat) send(id pb.HandsMessage, msg proto.Message) {
	s.t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(s.t, err)
	s.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: "hands", Id: uint32(id), Payload: payload}, //nolint:gosec // a small enum
	}})
}

func (s *seat) read() *wire.ServerFrame {
	s.t.Helper()
	_, data, err := s.ws.Read(testContext(s.t))
	require.NoError(s.t, err)
	s.frames = append(s.frames, data)

	frame := &wire.ServerFrame{}
	require.NoError(s.t, proto.Unmarshal(data, frame))
	if state := frame.GetState(); state != nil {
		s.latest = decodeState(s.t, state)
	}
	s.checkHidden(data)
	return frame
}

func (s *seat) readState() {
	s.t.Helper()
	frame := s.read()
	require.NotNil(s.t, frame.GetState(), "got %v", frame)
}

// readRefusal reads the error for a refused move, and then the state that follows every turn.
func (s *seat) readRefusal() *wire.Error {
	s.t.Helper()
	got := s.read().GetError()
	require.NotNil(s.t, got)
	s.readState()
	return got
}

func decodeState(t *testing.T, state *wire.RoomState) snapshot {
	t.Helper()
	got := snapshot{deck: &pb.DeckView{}, turns: &pb.TurnsView{}, hand: &pb.HandsView{}}
	for _, view := range state.GetViews() {
		var target proto.Message
		switch view.GetModule() {
		case "deck":
			target = got.deck
		case "turns":
			target = got.turns
		case "hands":
			target = got.hand
		default:
			require.FailNow(t, "unknown view", view.GetModule())
		}
		require.NoError(t, proto.Unmarshal(view.GetView(), target))
	}
	return got
}

// checkHidden is the hidden-card test. Cards have names long enough to find in raw bytes, so a card can't slip
// through in any field or any frame: a player may only ever receive their own cards and the top card.
func (s *seat) checkHidden(data []byte) {
	s.t.Helper()
	assert.False(s.t, bytes.Contains(data, testSeed[:]), "a frame contains the room's seed")
	for _, card := range rules.NewDeck() {
		if !bytes.Contains(data, []byte(card)) {
			continue
		}
		visible := card == s.latest.deck.GetTop() || slices.Contains(s.latest.hand.GetCards(), card)
		assert.True(s.t, visible, "a frame shows %s to a player who can't see it", card)
	}
}

func (g *game) current() (actor, waiting *seat) {
	g.t.Helper()
	switch g.first.latest.turns.GetCurrent() {
	case g.first.id:
		return g.first, g.other
	case g.other.id:
		return g.other, g.first
	default:
		require.FailNow(g.t, "nobody's turn")
		return nil, nil
	}
}

// move makes one valid move, and reads the state both players then receive.
func (g *game) move() {
	g.t.Helper()
	actor, waiting := g.current()
	view := actor.latest.hand
	switch {
	case len(view.GetPlayable()) > 0:
		actor.send(pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: view.GetPlayable()[0], ChosenSuit: "hearts"})
	case view.GetCanDraw():
		actor.send(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	default:
		require.True(g.t, view.GetCanPass(), "no move is possible")
		actor.send(pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{})
	}
	actor.readState()
	waiting.readState()
	g.checkConsistent()
}

// checkConsistent proves the views can't be right by accident, or pass the hidden-card test by showing nothing.
func (g *game) checkConsistent() {
	g.t.Helper()
	first, other := g.first.latest, g.other.latest

	for _, card := range first.hand.GetCards() {
		assert.NotContains(g.t, other.hand.GetCards(), card, "both players hold a card")
	}
	assert.Len(g.t, other.hand.GetCards(), int(first.hand.GetOthers()[0].GetCount()))
	assert.Len(g.t, first.hand.GetCards(), int(other.hand.GetOthers()[0].GetCount()))
	assert.Equal(g.t, first.deck.GetTop(), other.deck.GetTop())
	assert.Equal(g.t, first.turns.GetCurrent(), other.turns.GetCurrent())

	held := len(first.hand.GetCards()) + len(other.hand.GetCards())
	assert.LessOrEqual(g.t, held+int(first.deck.GetDrawCount())+1, totalCards)
}

func (g *game) reconnect(s *seat) *seat {
	g.t.Helper()
	require.NoError(g.t, s.ws.CloseNow())
	back := connect(g.t, g.url, s.id, s.secret)
	assert.Equal(g.t, s.id, back.id)
	back.join()
	back.readState()

	waiting := g.first
	if s == g.first {
		g.first = back
		waiting = g.other
	} else {
		g.other = back
	}
	waiting.readState() // the turn that seated the returning player
	return back
}

func TestGameStartsWithBothPlayersDealtIn(t *testing.T) {
	g := startGame(t, testSeed)

	for _, s := range []*seat{g.first, g.other} {
		assert.Len(t, s.latest.hand.GetCards(), rules.HandSize)
		assert.True(t, s.latest.turns.GetStarted())
		assert.NotEmpty(t, s.latest.deck.GetTop())
		assert.False(t, rules.IsWild(s.latest.deck.GetTop()), "an eight can't start the game")
	}
	assert.Equal(t, g.first.id, g.first.latest.turns.GetCurrent(), "whoever arrived first goes first")
	assert.Equal(t, uint32(totalCards-2*rules.HandSize-1), g.first.latest.deck.GetDrawCount())
	g.checkConsistent()
}

func TestWholeGameWithAReconnectShowsNobodyAnyHiddenCards(t *testing.T) {
	g := startGame(t, testSeed)

	for move := range maxMoves {
		if g.first.latest.turns.GetWinner() != "" {
			break
		}
		if move == reconnectTurn {
			before := g.first.latest
			back := g.reconnect(g.first)
			assert.Equal(t, before.hand.GetCards(), back.latest.hand.GetCards(), "the seat and the cards are kept")
			assert.Equal(t, before.turns.GetCurrent(), back.latest.turns.GetCurrent())
		}
		g.move()
	}

	winner := g.first.latest.turns.GetWinner()
	require.NotEmpty(t, winner, "the game didn't finish")
	assert.Equal(t, winner, g.other.latest.turns.GetWinner())

	winnerSeat, loserSeat := g.first, g.other
	if winner == g.other.id {
		winnerSeat, loserSeat = g.other, g.first
	}
	assert.Empty(t, winnerSeat.latest.hand.GetCards())
	assert.NotEmpty(t, loserSeat.latest.hand.GetCards())
	assert.Empty(t, g.first.latest.turns.GetCurrent(), "nobody moves after the game ends")

	// Both players did receive the game, so the test can't pass by nothing being sent.
	assert.Greater(t, len(g.first.frames), 1)
	assert.Greater(t, len(g.other.frames), 1)

	// Nobody can move once it's over.
	loserSeat.send(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	assert.Equal(t, hands.ErrNotYourTurn.Error(), loserSeat.readRefusal().GetMessage())
}

func TestRefusedMoves(t *testing.T) {
	tests := []struct {
		name string
		// outOfTurn sends the move from the player who is waiting, instead of the one whose turn it is.
		outOfTurn bool
		id        pb.HandsMessage
		msg       func(waiting *seat) proto.Message
		want      error
	}{
		{
			name:      "playing out of turn",
			outOfTurn: true,
			id:        pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:       func(waiting *seat) proto.Message { return &pb.Play{Card: waiting.latest.hand.GetCards()[0]} },
			want:      hands.ErrNotYourTurn,
		},
		{
			name:      "drawing out of turn",
			outOfTurn: true,
			id:        pb.HandsMessage_HANDS_MESSAGE_DRAW,
			msg:       func(_ *seat) proto.Message { return &pb.Draw{} },
			want:      hands.ErrNotYourTurn,
		},
		{
			name: "playing the other player's card",
			id:   pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:  func(waiting *seat) proto.Message { return &pb.Play{Card: waiting.latest.hand.GetCards()[0]} },
			want: hands.ErrNotInHand,
		},
		{
			name: "playing a made-up card",
			id:   pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:  func(_ *seat) proto.Message { return &pb.Play{Card: "joker"} },
			want: hands.ErrNotInHand,
		},
		{
			name: "passing without drawing",
			id:   pb.HandsMessage_HANDS_MESSAGE_PASS,
			msg:  func(_ *seat) proto.Message { return &pb.Pass{} },
			want: hands.ErrMustDraw,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := startGame(t, testSeed)
			actor, waiting := g.current()
			sender, receiver := actor, waiting
			if tt.outOfTurn {
				sender, receiver = waiting, actor
			}
			before := actor.latest.hand.GetCards()

			sender.send(tt.id, tt.msg(waiting))

			assert.Equal(t, tt.want.Error(), sender.readRefusal().GetMessage())
			receiver.readState()
			assert.Equal(t, before, actor.latest.hand.GetCards(), "a refused move changes nothing")
			assert.Equal(t, actor.id, actor.latest.turns.GetCurrent(), "a refused move keeps the turn")
		})
	}
}

func TestDrawingTwiceInATurnIsRefused(t *testing.T) {
	g := startGame(t, testSeed)
	actor, waiting := g.current()

	actor.send(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	actor.readState()
	waiting.readState()
	assert.Len(t, actor.latest.hand.GetCards(), rules.HandSize+1)
	assert.False(t, actor.latest.hand.GetCanDraw())
	assert.True(t, actor.latest.hand.GetCanPass())

	actor.send(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	assert.Equal(t, hands.ErrAlreadyDrew.Error(), actor.readRefusal().GetMessage())
	waiting.readState()

	actor.send(pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{})
	actor.readState()
	waiting.readState()
	assert.Equal(t, waiting.id, actor.latest.turns.GetCurrent())
}

func TestCardsThatDoNotMatchAreRefused(t *testing.T) {
	g := startGame(t, testSeed)
	actor, waiting := g.current()

	var unplayable string
	for _, card := range actor.latest.hand.GetCards() {
		if !slices.Contains(actor.latest.hand.GetPlayable(), card) {
			unplayable = card
			break
		}
	}
	require.NotEmpty(t, unplayable, "this seed should deal a card that doesn't match")

	actor.send(pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: unplayable})

	assert.Equal(t, hands.ErrNotPlayable.Error(), actor.readRefusal().GetMessage())
	waiting.readState()
}

func TestEightsNeedAValidSuitAndSetIt(t *testing.T) {
	// Look for a seed that deals the first player an eight.
	for seed := range byte(100) {
		g := startGame(t, [32]byte{seed})
		actor, waiting := g.current()
		at := slices.IndexFunc(actor.latest.hand.GetCards(), rules.IsWild)
		if at < 0 {
			continue
		}
		eight := actor.latest.hand.GetCards()[at]
		chosen := "spades"
		if rules.Suit(eight) == chosen {
			chosen = "clubs"
		}

		for _, bad := range []string{"", "stars"} {
			actor.send(pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: eight, ChosenSuit: bad})
			assert.Equal(t, hands.ErrChooseSuit.Error(), actor.readRefusal().GetMessage())
			waiting.readState()
		}

		actor.send(pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: eight, ChosenSuit: chosen})
		actor.readState()
		waiting.readState()

		assert.Equal(t, eight, waiting.latest.deck.GetTop())
		assert.Equal(t, chosen, waiting.latest.deck.GetActiveSuit(), "the chosen suit replaces the eight's own")
		return
	}
	require.FailNow(t, "no seed dealt an eight")
}

func TestSameSeedDealsTheSameGame(t *testing.T) {
	first := startGame(t, testSeed)
	second := startGame(t, testSeed)
	different := startGame(t, [32]byte{1})

	assert.Equal(t, first.first.latest.hand.GetCards(), second.first.latest.hand.GetCards())
	assert.Equal(t, first.other.latest.hand.GetCards(), second.other.latest.hand.GetCards())
	assert.Equal(t, first.first.latest.deck.GetTop(), second.first.latest.deck.GetTop())
	assert.NotEqual(t, first.first.latest.hand.GetCards(), different.first.latest.hand.GetCards())
}

func TestGameIsSavedWithEverythingItNeeds(t *testing.T) {
	records := memory.New()
	url, _ := startServerWith(t, testSeed, records)
	g := startGameOn(t, url)
	for range reconnectTurn {
		g.move()
	}

	var saved []string
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
		saved, modules = state.GetHands()[g.first.id].GetCards(), snapshot.GetModules()
		return slices.Equal(saved, g.first.latest.hand.GetCards())
	}, testTimeout, 10*time.Millisecond, "the saved hand never caught up with the game")

	names := make([]string, 0, len(modules))
	for _, module := range modules {
		names = append(names, module.GetModule())
		assert.Equal(t, uint32(1), module.GetVersion())
	}
	assert.Equal(t, []string{"deck", "turns", "hands"}, names)
}

// transcript plays a whole game, recording what each seat sees. It leaves out player IDs, which differ between runs.
func transcript(t *testing.T, g *game, afterMove func(move int)) []string {
	t.Helper()
	lines := make([]string, 0, maxMoves)
	record := func() {
		first, second := g.first.latest, g.other.latest
		lines = append(lines, fmt.Sprintf("top=%s suit=%s draw=%d first=%v second=%v",
			first.deck.GetTop(), first.deck.GetActiveSuit(), first.deck.GetDrawCount(),
			first.hand.GetCards(), second.hand.GetCards()))
	}
	record()
	for move := range maxMoves {
		if g.first.latest.turns.GetWinner() != "" {
			return lines
		}
		g.move()
		record()
		if afterMove != nil {
			afterMove(move)
		}
	}
	require.FailNow(t, "the game didn't finish")
	return nil
}

func TestGameRestartedPartwayMatchesAnUninterruptedOneCardForCard(t *testing.T) {
	want := transcript(t, startGame(t, testSeed), nil)
	require.Greater(t, len(want), reconnectTurn, "the game has to last past the restart")

	records := memory.New()
	url, stop := startServerWith(t, testSeed, records)
	g := startGameOn(t, url)

	got := transcript(t, g, func(move int) {
		if move != reconnectTurn {
			return
		}
		before := g.first.latest
		stop()

		// The new server has the store, but not the seed: everything has to come from the snapshot.
		newURL, _ := startServerWith(t, [32]byte{}, records)
		first := connect(t, newURL, g.first.id, g.first.secret)
		other := connect(t, newURL, g.other.id, g.other.secret)
		first.join()
		first.readState()
		other.join()
		other.readState()
		first.readState()
		g.url, g.first, g.other = newURL, first, other

		assert.Equal(t, before.hand.GetCards(), first.latest.hand.GetCards())
	})

	assert.Equal(t, want, got)
}

func TestRestoredGameShowsNobodyAnyHiddenCards(t *testing.T) {
	records := memory.New()
	url, stop := startServerWith(t, testSeed, records)
	g := startGameOn(t, url)
	g.move()
	stop()

	newURL, _ := startServerWith(t, [32]byte{}, records)
	first := connect(t, newURL, g.first.id, g.first.secret)
	first.join()

	// connect and join read through read, which runs the hidden-card check on every frame.
	first.readState()
	assert.Len(t, first.latest.hand.GetOthers(), 1)
	assert.NotEmpty(t, first.latest.hand.GetCards())
}

// startTimedGame is a game with these times.
func startTimedGame(t *testing.T, options cards.Options) *game {
	t.Helper()
	rt := playhold.NewRuntime(playhold.Config{
		Logger:  slog.New(slog.DiscardHandler),
		NewSeed: func() [32]byte { return testSeed },
	})
	typ, err := cards.NewRoomTypeWith(options)
	require.NoError(t, err)
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{RoomType: typ, MemoryLimit: -1})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		assert.NoError(t, rt.Shutdown(ctx))
	})
	return startGameOn(t, "ws"+strings.TrimPrefix(httpServer.URL, "http"))
}

func TestPlayerWhoRunsOutOfTimeDrawsAndPasses(t *testing.T) {
	g := startTimedGame(t, cards.Options{TurnTime: 100 * time.Millisecond})
	actor, waiting := g.current()
	require.Len(t, actor.latest.hand.GetCards(), rules.HandSize)

	actor.readState()
	waiting.readState()

	assert.Len(t, actor.latest.hand.GetCards(), rules.HandSize+1, "they drew")
	assert.Equal(t, waiting.id, actor.latest.turns.GetCurrent(), "and passed")
	g.checkConsistent()

	// The next player's time starts in turn.
	actor.readState()
	waiting.readState()

	assert.Len(t, waiting.latest.hand.GetCards(), rules.HandSize+1)
	assert.Equal(t, actor.id, actor.latest.turns.GetCurrent())
}

func TestPlayerWhoAlreadyDrewOnlyPassesWhenTimeRunsOut(t *testing.T) {
	g := startTimedGame(t, cards.Options{TurnTime: 300 * time.Millisecond})
	actor, waiting := g.current()

	actor.send(pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	actor.readState()
	waiting.readState()
	require.Len(t, actor.latest.hand.GetCards(), rules.HandSize+1)

	actor.readState()
	waiting.readState()

	assert.Len(t, actor.latest.hand.GetCards(), rules.HandSize+1, "no second card")
	assert.Equal(t, waiting.id, actor.latest.turns.GetCurrent())
}

func TestViewShowsTheTimeLeftInTheTurn(t *testing.T) {
	turnTime := time.Hour
	g := startTimedGame(t, cards.Options{TurnTime: turnTime})

	turns := g.first.latest.turns
	assert.Equal(t, turnTime.Milliseconds(), turns.GetTurnMillisTotal())
	assert.Positive(t, turns.GetTurnMillisLeft())
	assert.LessOrEqual(t, turns.GetTurnMillisLeft(), turnTime.Milliseconds())
}

func TestFinishedGameClosesItsRoomAfterAWhile(t *testing.T) {
	g := startTimedGame(t, cards.Options{FinishedRoomTime: 50 * time.Millisecond})
	for g.first.latest.turns.GetWinner() == "" {
		g.move()
	}

	// The turn that closes the room is a turn like any other, so its view comes first, and RoomClosed comes last.
	for _, s := range []*seat{g.first, g.other} {
		frame := s.read()
		for frame.GetClosed() == nil {
			require.NotNil(t, frame.GetState(), "got %v", frame)
			frame = s.read()
		}
		assert.Equal(t, wire.CloseReason_CLOSE_REASON_REQUESTED, frame.GetClosed().GetReason())
	}
}

func TestThirdPlayerIsRefused(t *testing.T) {
	g := startGame(t, testSeed)

	third := connect(t, g.url, "", "")
	third.sendFrame(&wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: testRoom}}})

	assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_FULL, third.read().GetError().GetCode())
}

package cards_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/hands"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
	"github.com/ny4rl4th0t3p/playhold/playholdtest"
)

// These tests play the game in a room under test, without a server; server_test.go covers what needs one.

const (
	maxMoves      = 5000
	totalCards    = 52
	reconnectTurn = 5
)

// testSeed is recognizable in raw bytes, so the hidden-card test can look for it.
var testSeed = [32]byte([]byte("playhold-test-seed-do-not-leak!!"))

// player is one seat at the table. It checks every state it receives for hidden information.
type player struct {
	t       *testing.T
	p       *playholdtest.Player
	checked int // how many of the received states have been checked
}

func (s *player) id() string { return string(s.p.ID()) }

func (s *player) deck() *pb.DeckView   { return playholdtest.View[pb.DeckView](s.p, "deck") }
func (s *player) turns() *pb.TurnsView { return playholdtest.View[pb.TurnsView](s.p, "turns") }
func (s *player) hand() *pb.HandsView  { return playholdtest.View[pb.HandsView](s.p, "hands") }

// checkHidden is the hidden-card test, run after every turn on the states received since the last check. Cards have
// names long enough to find in raw bytes, so a card can't slip through in any field: a player may only ever receive
// their own cards and the top card. It compares those states with the latest views, so it has to run after each turn
// that changes the cards, before the next one: table.send does that for every move.
func (s *player) checkHidden() {
	s.t.Helper()
	received := s.p.Received()
	for _, data := range received[s.checked:] {
		assert.False(s.t, bytes.Contains(data, testSeed[:]), "a state contains the room's seed")
		for _, card := range rules.NewDeck() {
			if !bytes.Contains(data, []byte(card)) {
				continue
			}
			visible := card == s.deck().GetTop() || slices.Contains(s.hand().GetCards(), card)
			assert.True(s.t, visible, "a state shows %s to a player who can't see it", card)
		}
	}
	s.checked = len(received)
}

// table is a two-player game in a room under test.
type table struct {
	t     *testing.T
	room  *playholdtest.Room
	first *player
	other *player
}

func newTable(t *testing.T, options cards.Options, opts playholdtest.Options) *table {
	t.Helper()
	typ, err := cards.NewRoomTypeWith(options)
	require.NoError(t, err)
	if opts.Seed == ([32]byte{}) {
		opts.Seed = testSeed
	}
	g := &table{t: t, room: playholdtest.NewRoom(t, typ, opts)}
	g.first = g.join("alice")
	g.other = g.join("bob") // the game starts
	g.checkHidden()
	return g
}

func startGame(t *testing.T, seed [32]byte) *table {
	t.Helper()
	return newTable(t, cards.Options{}, playholdtest.Options{Seed: seed})
}

func (g *table) join(id playhold.PlayerID) *player {
	g.t.Helper()
	return &player{t: g.t, p: g.room.Join(id)}
}

func (g *table) checkHidden() {
	g.t.Helper()
	g.first.checkHidden()
	g.other.checkHidden()
}

// send sends a move as the player, and checks the state its turn sent both players, refused or not.
func (g *table) send(s *player, id pb.HandsMessage, msg proto.Message) error {
	g.t.Helper()
	err := s.p.Send("hands", uint32(id), msg) //nolint:gosec // a small enum
	g.checkHidden()
	return err
}

func (g *table) current() (actor, waiting *player) {
	g.t.Helper()
	switch g.first.turns().GetCurrent() {
	case g.first.id():
		return g.first, g.other
	case g.other.id():
		return g.other, g.first
	default:
		require.FailNow(g.t, "nobody's turn")
		return nil, nil
	}
}

// move makes one valid move.
func (g *table) move() {
	g.t.Helper()
	actor, _ := g.current()
	view := actor.hand()
	var err error
	switch {
	case len(view.GetPlayable()) > 0:
		err = g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: view.GetPlayable()[0], ChosenSuit: "hearts"})
	case view.GetCanDraw():
		err = g.send(actor, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{})
	default:
		require.True(g.t, view.GetCanPass(), "no move is possible")
		err = g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{})
	}
	require.NoError(g.t, err)
	g.checkConsistent()
}

// checkConsistent proves the views can't be right by accident, or pass the hidden-card test by showing nothing.
func (g *table) checkConsistent() {
	g.t.Helper()
	first, other := g.first, g.other

	for _, card := range first.hand().GetCards() {
		assert.NotContains(g.t, other.hand().GetCards(), card, "both players hold a card")
	}
	assert.Len(g.t, other.hand().GetCards(), int(first.hand().GetOthers()[0].GetCount()))
	assert.Len(g.t, first.hand().GetCards(), int(other.hand().GetOthers()[0].GetCount()))
	assert.Equal(g.t, first.deck().GetTop(), other.deck().GetTop())
	assert.Equal(g.t, first.turns().GetCurrent(), other.turns().GetCurrent())

	held := len(first.hand().GetCards()) + len(other.hand().GetCards())
	assert.LessOrEqual(g.t, held+int(first.deck().GetDrawCount())+1, totalCards)
}

// reconnect drops the player's connection and brings them back.
func (g *table) reconnect(s *player) *player {
	g.t.Helper()
	s.p.Disconnect()
	back := g.join(s.p.ID())
	if s == g.first {
		g.first = back
	} else {
		g.other = back
	}
	g.checkHidden()
	return back
}

// rejoin brings both players back after a restart, and checks that the game is where it was.
func (g *table) rejoin(before *pb.HandsView, current string) {
	g.t.Helper()
	g.first, g.other = g.join("alice"), g.join("bob")
	g.checkHidden()
	assert.Equal(g.t, before.GetCards(), g.first.hand().GetCards())
	assert.Equal(g.t, current, g.first.turns().GetCurrent())
}

func TestGameStartsWithBothPlayersDealtIn(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := startGame(t, testSeed)

		for _, s := range []*player{g.first, g.other} {
			assert.Len(t, s.hand().GetCards(), rules.HandSize)
			assert.True(t, s.turns().GetStarted())
			assert.NotEmpty(t, s.deck().GetTop())
			assert.False(t, rules.IsWild(s.deck().GetTop()), "an eight can't start the game")
		}
		assert.Equal(t, g.first.id(), g.first.turns().GetCurrent(), "whoever arrived first goes first")
		assert.Equal(t, uint32(totalCards-2*rules.HandSize-1), g.first.deck().GetDrawCount())
		g.checkConsistent()
	})
}

func TestWholeGameWithAReconnectShowsNobodyAnyHiddenCards(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := startGame(t, testSeed)

		for move := range maxMoves {
			if g.first.turns().GetWinner() != "" {
				break
			}
			if move == reconnectTurn {
				cardsBefore, currentBefore := g.first.hand().GetCards(), g.first.turns().GetCurrent()
				back := g.reconnect(g.first)
				assert.Equal(t, cardsBefore, back.hand().GetCards(), "the seat and the cards are kept")
				assert.Equal(t, currentBefore, back.turns().GetCurrent())
			}
			g.move()
		}

		winner := g.first.turns().GetWinner()
		require.NotEmpty(t, winner, "the game didn't finish")
		assert.Equal(t, winner, g.other.turns().GetWinner())

		winnerSeat, loserSeat := g.first, g.other
		if winner == g.other.id() {
			winnerSeat, loserSeat = g.other, g.first
		}
		assert.Empty(t, winnerSeat.hand().GetCards())
		assert.NotEmpty(t, loserSeat.hand().GetCards())
		assert.Empty(t, g.first.turns().GetCurrent(), "nobody moves after the game ends")

		// Both players did receive the game, so the test can't pass by nothing being sent.
		assert.Greater(t, len(g.first.p.Received()), 1)
		assert.Greater(t, len(g.other.p.Received()), 1)

		// Nobody can move once it's over.
		require.ErrorIs(t, g.send(loserSeat, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}), hands.ErrNotYourTurn)
	})
}

func TestRefusedMoves(t *testing.T) {
	tests := []struct {
		name string
		// outOfTurn sends the move from the player who is waiting, instead of the one whose turn it is.
		outOfTurn bool
		id        pb.HandsMessage
		msg       func(waiting *player) proto.Message
		want      error
	}{
		{
			name:      "playing out of turn",
			outOfTurn: true,
			id:        pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:       func(waiting *player) proto.Message { return &pb.Play{Card: waiting.hand().GetCards()[0]} },
			want:      hands.ErrNotYourTurn,
		},
		{
			name:      "drawing out of turn",
			outOfTurn: true,
			id:        pb.HandsMessage_HANDS_MESSAGE_DRAW,
			msg:       func(_ *player) proto.Message { return &pb.Draw{} },
			want:      hands.ErrNotYourTurn,
		},
		{
			name: "playing the other player's card",
			id:   pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:  func(waiting *player) proto.Message { return &pb.Play{Card: waiting.hand().GetCards()[0]} },
			want: hands.ErrNotInHand,
		},
		{
			name: "playing a made-up card",
			id:   pb.HandsMessage_HANDS_MESSAGE_PLAY,
			msg:  func(_ *player) proto.Message { return &pb.Play{Card: "joker"} },
			want: hands.ErrNotInHand,
		},
		{
			name: "passing without drawing",
			id:   pb.HandsMessage_HANDS_MESSAGE_PASS,
			msg:  func(_ *player) proto.Message { return &pb.Pass{} },
			want: hands.ErrMustDraw,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			playholdtest.Run(t, func(t *testing.T) {
				g := startGame(t, testSeed)
				actor, waiting := g.current()
				sender := actor
				if tt.outOfTurn {
					sender = waiting
				}
				before := actor.hand().GetCards()

				require.ErrorIs(t, g.send(sender, tt.id, tt.msg(waiting)), tt.want)

				assert.Equal(t, before, actor.hand().GetCards(), "a refused move changes nothing")
				assert.Equal(t, actor.id(), actor.turns().GetCurrent(), "a refused move keeps the turn")
			})
		})
	}
}

func TestDrawingTwiceInATurnIsRefused(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := startGame(t, testSeed)
		actor, waiting := g.current()

		require.NoError(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}))
		assert.Len(t, actor.hand().GetCards(), rules.HandSize+1)
		assert.False(t, actor.hand().GetCanDraw())
		assert.True(t, actor.hand().GetCanPass())

		require.ErrorIs(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}), hands.ErrAlreadyDrew)

		require.NoError(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{}))
		assert.Equal(t, waiting.id(), actor.turns().GetCurrent())
	})
}

func TestCardsThatDoNotMatchAreRefused(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := startGame(t, testSeed)
		actor, _ := g.current()

		var unplayable string
		for _, card := range actor.hand().GetCards() {
			if !slices.Contains(actor.hand().GetPlayable(), card) {
				unplayable = card
				break
			}
		}
		require.NotEmpty(t, unplayable, "this seed should deal a card that doesn't match")

		require.ErrorIs(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: unplayable}), hands.ErrNotPlayable)
	})
}

func TestEightsNeedAValidSuitAndSetIt(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		// Look for a seed that deals the first player an eight. The modules keep their state to themselves, so a test
		// from outside them can't deal one.
		for seed := range byte(100) {
			g := startGame(t, [32]byte{1, seed})
			actor, waiting := g.current()
			at := slices.IndexFunc(actor.hand().GetCards(), rules.IsWild)
			if at < 0 {
				continue
			}
			eight := actor.hand().GetCards()[at]
			chosen := "spades"
			if rules.Suit(eight) == chosen {
				chosen = "clubs"
			}

			for _, bad := range []string{"", "stars"} {
				err := g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: eight, ChosenSuit: bad})
				require.ErrorIs(t, err, hands.ErrChooseSuit)
			}

			require.NoError(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: eight, ChosenSuit: chosen}))

			assert.Equal(t, eight, waiting.deck().GetTop())
			assert.Equal(t, chosen, waiting.deck().GetActiveSuit(), "the chosen suit replaces the eight's own")
			return
		}
		require.FailNow(t, "no seed dealt an eight")
	})
}

func TestSameSeedDealsTheSameGame(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		first := startGame(t, testSeed)
		second := startGame(t, testSeed)
		different := startGame(t, [32]byte{1})

		assert.Equal(t, first.first.hand().GetCards(), second.first.hand().GetCards())
		assert.Equal(t, first.other.hand().GetCards(), second.other.hand().GetCards())
		assert.Equal(t, first.first.deck().GetTop(), second.first.deck().GetTop())
		assert.NotEqual(t, first.first.hand().GetCards(), different.first.hand().GetCards())
	})
}

// transcript plays a whole game, recording what each player sees.
func transcript(t *testing.T, g *table, afterMove func(move int)) []string {
	t.Helper()
	lines := make([]string, 0, maxMoves)
	record := func() {
		first, second := g.first, g.other
		lines = append(lines, fmt.Sprintf("top=%s suit=%s draw=%d first=%v second=%v",
			first.deck().GetTop(), first.deck().GetActiveSuit(), first.deck().GetDrawCount(),
			first.hand().GetCards(), second.hand().GetCards()))
	}
	record()
	for move := range maxMoves {
		if g.first.turns().GetWinner() != "" {
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

// transcriptRestartedAs plays a game from these options, restarting it partway onto a room type with those.
func transcriptRestartedAs(t *testing.T, from, to cards.Options) []string {
	t.Helper()
	g := newTable(t, from, playholdtest.Options{})
	typ, err := cards.NewRoomTypeWith(to)
	require.NoError(t, err)

	return transcript(t, g, func(move int) {
		if move == reconnectTurn {
			before, current := g.first.hand(), g.first.turns().GetCurrent()
			g.room.RestartAs(typ)
			g.rejoin(before, current)
		}
	})
}

func TestGameRestartedPartwayMatchesAnUninterruptedOneCardForCard(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		want := transcript(t, startGame(t, testSeed), nil)
		require.Greater(t, len(want), reconnectTurn, "the game has to last past the restart")

		assert.Equal(t, want, transcriptRestartedAs(t, cards.Options{}, cards.Options{}))
	})
}

func TestDeployThatChangesTheSnapshotFormatMatchesCardForCard(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		want := transcript(t, startGame(t, testSeed), nil)

		assert.Equal(t, want, transcriptRestartedAs(t, cards.Options{}, cards.Options{TurnsSnapshotVersion: 2}))
	})
}

func TestSnapshotFormatTwoSurvivesARestartOfItsOwn(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		version2 := cards.Options{TurnsSnapshotVersion: 2}
		want := transcript(t, startGame(t, testSeed), nil)

		assert.Equal(t, want, transcriptRestartedAs(t, version2, version2))
	})
}

func TestRestoredGameShowsNobodyAnyHiddenCards(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := startGame(t, testSeed)
		g.move()

		g.room.Restart()
		first := g.join("alice")

		first.checkHidden()
		assert.Len(t, first.hand().GetOthers(), 1)
		assert.NotEmpty(t, first.hand().GetCards())
	})
}

func TestPlayerWhoRunsOutOfTimeDrawsAndPasses(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := newTable(t, cards.Options{TurnTime: time.Minute}, playholdtest.Options{})
		actor, waiting := g.current()
		require.Len(t, actor.hand().GetCards(), rules.HandSize)

		g.room.Advance(time.Minute)
		g.checkHidden()

		assert.Len(t, actor.hand().GetCards(), rules.HandSize+1, "they drew")
		assert.Equal(t, waiting.id(), actor.turns().GetCurrent(), "and passed")
		g.checkConsistent()

		// The next player's time starts in turn.
		g.room.Advance(time.Minute)
		g.checkHidden()

		assert.Len(t, waiting.hand().GetCards(), rules.HandSize+1)
		assert.Equal(t, actor.id(), actor.turns().GetCurrent())
	})
}

func TestPlayerWhoAlreadyDrewOnlyPassesWhenTimeRunsOut(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := newTable(t, cards.Options{TurnTime: time.Minute}, playholdtest.Options{})
		actor, waiting := g.current()

		require.NoError(t, g.send(actor, pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}))
		require.Len(t, actor.hand().GetCards(), rules.HandSize+1)

		g.room.Advance(time.Minute)
		g.checkHidden()

		assert.Len(t, actor.hand().GetCards(), rules.HandSize+1, "no second card")
		assert.Equal(t, waiting.id(), actor.turns().GetCurrent())
	})
}

func TestViewShowsTheTimeLeftInTheTurn(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		turnTime := time.Hour
		g := newTable(t, cards.Options{TurnTime: turnTime}, playholdtest.Options{})

		turns := g.first.turns()
		assert.Equal(t, turnTime.Milliseconds(), turns.GetTurnMillisTotal())
		assert.Equal(t, turnTime.Milliseconds(), turns.GetTurnMillisLeft(), "no time has passed since the turn began")
	})
}

func TestFinishedGameClosesItsRoomAfterAWhile(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		g := newTable(t, cards.Options{FinishedRoomTime: time.Minute}, playholdtest.Options{})
		for g.first.turns().GetWinner() == "" {
			g.move()
		}

		g.room.Advance(time.Minute - time.Second)
		_, closed := g.room.Closed()
		require.False(t, closed)

		g.room.Advance(time.Second)
		reason, closed := g.room.Closed()
		require.True(t, closed)
		assert.Equal(t, playhold.CloseRequested, reason)
	})
}

func TestOtherPlayerSeesSomeoneGoAndComeBack(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		grace := playholdtest.Options{DisconnectGrace: playhold.Timeout{After: time.Second}}
		g := newTable(t, cards.Options{}, grace)
		assert.Empty(t, g.first.turns().GetAway())

		g.other.p.Disconnect()
		g.room.Advance(time.Second)
		assert.Equal(t, []string{g.other.id()}, g.first.turns().GetAway())

		back := g.join("bob")
		assert.Empty(t, g.first.turns().GetAway())
		assert.Empty(t, back.turns().GetAway())
	})
}

func TestPlayerWhoWasAwayIsStillAwayAfterARestart(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		grace := playholdtest.Options{DisconnectGrace: playhold.Timeout{After: time.Second}}
		g := newTable(t, cards.Options{}, grace)
		g.other.p.Disconnect()
		g.room.Advance(time.Second)
		require.Equal(t, []string{g.other.id()}, g.first.turns().GetAway())

		g.room.Restart()
		first := g.join("alice")
		assert.Equal(t, []string{g.other.id()}, first.turns().GetAway())

		back := g.join("bob")
		assert.Empty(t, first.turns().GetAway())
		assert.Empty(t, back.turns().GetAway())
	})
}

// Package deck owns the draw pile and the discard pile.
package deck

import (
	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
)

var key = playhold.NewKey[state]("deck")

type state struct {
	pile       []string // the next card to draw is first
	discard    []string // the top card is last
	activeSuit string
}

// Module registers the piles with a room.
type Module struct{}

// Name implements playhold.Module.
func (Module) Name() string { return "deck" }

// Register implements playhold.Module.
func (Module) Register(r *playhold.Registry) {
	playhold.State(r, key, func() *state { return &state{} })
	playhold.Snapshot(r, key, 1, save, load)
	playhold.On(r, start)
	playhold.View(r, view)
}

func save(s *state) *pb.DeckState {
	return &pb.DeckState{Pile: s.pile, Discard: s.discard, ActiveSuit: s.activeSuit}
}

func load(msg *pb.DeckState) *state {
	return &state{pile: msg.GetPile(), discard: msg.GetDiscard(), activeSuit: msg.GetActiveSuit()}
}

// start shuffles and turns over the first card. An eight can't start the game, because nobody has chosen a suit.
func start(rc *playhold.RoomContext, _ rules.GameStarted) {
	s := key.Get(rc)
	s.pile = rules.NewDeck()
	shuffle(rc, s.pile)

	first := 0
	for rules.IsWild(s.pile[first]) {
		first++
	}
	top := s.pile[first]
	s.pile = append(s.pile[:first], s.pile[first+1:]...)
	s.discard = []string{top}
	s.activeSuit = rules.Suit(top)
}

func shuffle(rc *playhold.RoomContext, cards []string) {
	rc.Rand().Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })
}

// view shows the top card and how many cards are left. The order of the pile stays private.
func view(rc *playhold.RoomContext) *pb.DeckView {
	s := key.Get(rc)
	return &pb.DeckView{Top: top(s), ActiveSuit: s.activeSuit, DrawCount: uint32(len(s.pile))} //nolint:gosec // at most 52
}

func top(s *state) string {
	if len(s.discard) == 0 {
		return ""
	}
	return s.discard[len(s.discard)-1]
}

// Keeper is what deck offers other modules.
type Keeper struct{}

// Draw takes up to n cards. When the pile runs out, the discard pile is shuffled into it, except for its top card.
func (Keeper) Draw(rc *playhold.RoomContext, n int) []string {
	s := key.Get(rc)
	drawn := make([]string, 0, n)
	for range n {
		if len(s.pile) == 0 {
			recycle(rc, s)
		}
		if len(s.pile) == 0 {
			break
		}
		drawn = append(drawn, s.pile[0])
		s.pile = s.pile[1:]
	}
	return drawn
}

func recycle(rc *playhold.RoomContext, s *state) {
	if len(s.discard) <= 1 {
		return
	}
	last := len(s.discard) - 1
	s.pile = append(s.pile, s.discard[:last]...)
	s.discard = []string{s.discard[last]}
	shuffle(rc, s.pile)
}

// Discard puts the card on top of the discard pile and sets the suit in play.
func (Keeper) Discard(rc *playhold.RoomContext, card, suit string) {
	s := key.Get(rc)
	s.discard = append(s.discard, card)
	s.activeSuit = suit
}

// Top returns the top card of the discard pile and the suit in play.
func (Keeper) Top(rc *playhold.RoomContext) (card, activeSuit string) {
	s := key.Get(rc)
	return top(s), s.activeSuit
}

// CanDraw reports whether any card can be drawn, counting those Draw would recycle.
func (Keeper) CanDraw(rc *playhold.RoomContext) bool {
	s := key.Get(rc)
	return len(s.pile) > 0 || len(s.discard) > 1
}

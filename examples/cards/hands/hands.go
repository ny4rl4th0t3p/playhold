// Package hands owns each player's cards and the moves they can make. It reaches the piles and the turn order only
// through the keepers it declares here, so it doesn't import those modules.
package hands

import (
	"errors"
	"slices"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
)

// Reasons a move is refused. They are sent to the player who tried it, so they never name a card.
var (
	ErrNotYourTurn = errors.New("it isn't your turn")
	ErrNotInHand   = errors.New("that card isn't in your hand")
	ErrNotPlayable = errors.New("that card can't go on the top card")
	ErrChooseSuit  = errors.New("an eight needs a suit to continue with")
	ErrAlreadyDrew = errors.New("you already drew this turn")
	ErrNothingLeft = errors.New("there's nothing left to draw")
	ErrMustDraw    = errors.New("you have to draw before passing")
)

// DeckKeeper is what hands needs from the deck module.
type DeckKeeper interface {
	Draw(rc *playhold.RoomContext, n int) []string
	Discard(rc *playhold.RoomContext, card, suit string)
	Top(rc *playhold.RoomContext) (card, activeSuit string)
	CanDraw(rc *playhold.RoomContext) bool
}

// TurnKeeper is what hands needs from the turns module.
type TurnKeeper interface {
	Current(rc *playhold.RoomContext) playhold.PlayerID
	Advance(rc *playhold.RoomContext)
}

var key = playhold.NewKey[state]("hands")

type state struct {
	cards map[playhold.PlayerID][]string
	drew  bool // the current player has drawn this turn
}

// Module registers the hands with a room.
type Module struct {
	Deck  DeckKeeper
	Turns TurnKeeper
}

// Name implements playhold.Module.
func (Module) Name() string { return "hands" }

// Register implements playhold.Module.
func (m Module) Register(r *playhold.Registry) {
	r.Needs("deck", m.Deck)
	r.Needs("turns", m.Turns)
	playhold.State(r, key, func() *state { return &state{cards: make(map[playhold.PlayerID][]string)} })
	playhold.Snapshot(r, key, 1, save, load)
	playhold.On(r, m.deal)
	playhold.Handle(r, uint32(pb.HandsMessage_HANDS_MESSAGE_PLAY), m.play)
	playhold.Handle(r, uint32(pb.HandsMessage_HANDS_MESSAGE_DRAW), m.draw)
	playhold.Handle(r, uint32(pb.HandsMessage_HANDS_MESSAGE_PASS), m.pass)
	playhold.View(r, m.view)
}

func save(s *state) *pb.HandsState {
	hands := make(map[string]*pb.Cards, len(s.cards))
	//playhold:order-independent copying into another map, so nothing depends on the order
	for player, cards := range s.cards {
		hands[string(player)] = &pb.Cards{Cards: cards}
	}
	return &pb.HandsState{Hands: hands, Drew: s.drew}
}

func load(msg *pb.HandsState) *state {
	cards := make(map[playhold.PlayerID][]string, len(msg.GetHands()))
	//playhold:order-independent copying into another map, so nothing depends on the order
	for player, hand := range msg.GetHands() {
		cards[playhold.PlayerID(player)] = hand.GetCards()
	}
	return &state{cards: cards, drew: msg.GetDrew()}
}

func (m Module) deal(rc *playhold.RoomContext, event rules.GameStarted) {
	s := key.Get(rc)
	for _, player := range event.Players {
		s.cards[player] = m.Deck.Draw(rc, rules.HandSize)
	}
}

func (m Module) play(rc *playhold.RoomContext, msg *pb.Play) error {
	if m.Turns.Current(rc) != rc.Player() {
		return ErrNotYourTurn
	}
	s := key.Get(rc)
	card := msg.GetCard()
	at := slices.Index(s.cards[rc.Player()], card)
	if at < 0 {
		return ErrNotInHand
	}
	top, activeSuit := m.Deck.Top(rc)
	if !rules.Playable(card, top, activeSuit) {
		return ErrNotPlayable
	}
	suit := rules.Suit(card)
	if rules.IsWild(card) {
		suit = msg.GetChosenSuit()
		if !rules.ValidSuit(suit) {
			return ErrChooseSuit
		}
	}

	s.cards[rc.Player()] = slices.Delete(s.cards[rc.Player()], at, at+1)
	m.Deck.Discard(rc, card, suit)
	s.drew = false
	if len(s.cards[rc.Player()]) == 0 {
		rc.Emit(rules.PlayerWon{Player: rc.Player()})
		return nil
	}
	m.Turns.Advance(rc)
	return nil
}

func (m Module) draw(rc *playhold.RoomContext, _ *pb.Draw) error {
	if m.Turns.Current(rc) != rc.Player() {
		return ErrNotYourTurn
	}
	s := key.Get(rc)
	if s.drew {
		return ErrAlreadyDrew
	}
	drawn := m.Deck.Draw(rc, 1)
	if len(drawn) == 0 {
		return ErrNothingLeft
	}
	s.cards[rc.Player()] = append(s.cards[rc.Player()], drawn...)
	s.drew = true
	return nil
}

func (m Module) pass(rc *playhold.RoomContext, _ *pb.Pass) error {
	if m.Turns.Current(rc) != rc.Player() {
		return ErrNotYourTurn
	}
	s := key.Get(rc)
	if !s.drew && m.Deck.CanDraw(rc) {
		return ErrMustDraw
	}
	s.drew = false
	m.Turns.Advance(rc)
	return nil
}

// view copies across only this player's own cards. Everyone else's hand is just a count.
func (m Module) view(rc *playhold.RoomContext) *pb.HandsView {
	s := key.Get(rc)
	player := rc.Player()
	view := &pb.HandsView{Cards: slices.Clone(s.cards[player])}

	if m.Turns.Current(rc) == player {
		top, activeSuit := m.Deck.Top(rc)
		for _, card := range s.cards[player] {
			if rules.Playable(card, top, activeSuit) {
				view.Playable = append(view.Playable, card)
			}
		}
		view.CanDraw = !s.drew && m.Deck.CanDraw(rc)
		view.CanPass = s.drew || !m.Deck.CanDraw(rc)
	}
	for _, other := range rc.Players() {
		if other != player {
			view.Others = append(view.Others, &pb.OtherHand{
				Player: string(other),
				Count:  uint32(len(s.cards[other])), //nolint:gosec // at most 52
			})
		}
	}
	return view
}

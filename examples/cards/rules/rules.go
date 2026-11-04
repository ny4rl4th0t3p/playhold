// Package rules holds the card rules and the events the game's modules share. It has no state or logic of its own
// beyond those rules, so any module can import it.
package rules

import (
	"slices"
	"strings"

	"github.com/ny4rl4th0t3p/playhold"
)

const (
	// Players is how many people play a game.
	Players = 2
	// HandSize is how many cards each player starts with.
	HandSize = 7

	wildRank  = "8"
	separator = "-of-"
)

var (
	ranks = []string{"A", "2", "3", "4", "5", "6", "7", wildRank, "9", "10", "J", "Q", "K"}
	suits = []string{"clubs", "diamonds", "hearts", "spades"}
)

// GameStarted is emitted when the room has all its players.
type GameStarted struct {
	Players []playhold.PlayerID
}

// PlayerWon is emitted when a player has no cards left.
type PlayerWon struct {
	Player playhold.PlayerID
}

// NewDeck returns all 52 cards in order. A card is a string such as "Q-of-spades".
func NewDeck() []string {
	deck := make([]string, 0, len(ranks)*len(suits))
	for _, suit := range suits {
		for _, rank := range ranks {
			deck = append(deck, rank+separator+suit)
		}
	}
	return deck
}

// Suit returns the card's suit.
func Suit(card string) string {
	_, suit, _ := strings.Cut(card, separator)
	return suit
}

func rank(card string) string {
	r, _, _ := strings.Cut(card, separator)
	return r
}

// IsWild reports whether the card can be played on anything.
func IsWild(card string) bool {
	return rank(card) == wildRank
}

// ValidSuit reports whether the string names a suit.
func ValidSuit(suit string) bool {
	return slices.Contains(suits, suit)
}

// Playable reports whether the card can go on the top card: an eight always can, and any other card has to match
// the top card's rank or the suit in play, which an eight may have changed.
func Playable(card, top, activeSuit string) bool {
	return IsWild(card) || rank(card) == rank(top) || Suit(card) == activeSuit
}

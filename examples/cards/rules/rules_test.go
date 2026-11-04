package rules

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlayable(t *testing.T) {
	tests := []struct {
		name       string
		card       string
		top        string
		activeSuit string
		want       bool
	}{
		{name: "same suit", card: "3-of-hearts", top: "K-of-hearts", activeSuit: "hearts", want: true},
		{name: "same rank", card: "K-of-spades", top: "K-of-hearts", activeSuit: "hearts", want: true},
		{name: "neither", card: "3-of-spades", top: "K-of-hearts", activeSuit: "hearts", want: false},
		{name: "eight on anything", card: "8-of-spades", top: "K-of-hearts", activeSuit: "hearts", want: true},
		{name: "chosen suit counts", card: "3-of-clubs", top: "8-of-hearts", activeSuit: "clubs", want: true},
		{name: "eight's printed suit doesn't", card: "3-of-hearts", top: "8-of-hearts", activeSuit: "clubs", want: false},
		{name: "ten isn't confused with its digits", card: "10-of-clubs", top: "A-of-hearts", activeSuit: "hearts", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Playable(tt.card, tt.top, tt.activeSuit))
		})
	}
}

func TestNewDeckHasEveryCardOnce(t *testing.T) {
	deck := NewDeck()
	seen := make(map[string]bool, len(deck))
	for _, card := range deck {
		seen[card] = true
	}

	assert.Len(t, deck, 52)
	assert.Len(t, seen, 52)
	assert.True(t, seen["8-of-hearts"])
	assert.True(t, seen["A-of-spades"])
}

// The hidden-card test looks for cards in raw bytes, which only works if no card's name is inside another's.
func TestNoCardNameContainsAnother(t *testing.T) {
	deck := NewDeck()
	for _, card := range deck {
		for _, other := range deck {
			if card != other {
				assert.False(t, strings.Contains(card, other), "%s contains %s", card, other)
			}
		}
	}
}

func TestSuitsAndWildcards(t *testing.T) {
	assert.Equal(t, "spades", Suit("Q-of-spades"))
	assert.True(t, IsWild("8-of-clubs"))
	assert.False(t, IsWild("Q-of-clubs"))
	assert.True(t, ValidSuit("clubs"))
	assert.False(t, ValidSuit("stars"))
	assert.False(t, ValidSuit(""))
}

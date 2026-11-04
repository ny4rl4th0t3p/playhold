// Package cards is a sample game, Crazy Eights for two players, built from three modules that know nothing of each
// other's packages. This file is the only place that sees all three, and wires them together.
package cards

import (
	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/deck"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/hands"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/turns"
)

// NewRoomType returns the room type for one game. The order matters: when a game starts, deck has to shuffle before
// hands deals.
func NewRoomType() (*playhold.RoomType, error) {
	return playhold.NewRoomType(playhold.RoomConfig{
		Name:       "crazy-eights",
		MaxPlayers: rules.Players,
		Modules: []playhold.Module{
			deck.Module{},
			turns.Module{},
			hands.Module{Deck: deck.Keeper{}, Turns: turns.Keeper{}},
		},
	})
}

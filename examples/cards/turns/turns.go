// Package turns owns whose turn it is, and whether the game is running.
package turns

import (
	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
)

var key = playhold.NewKey[state]("turns")

type state struct {
	players []playhold.PlayerID
	current int
	winner  playhold.PlayerID
}

// Module registers the turn order with a room.
type Module struct{}

// Name implements playhold.Module.
func (Module) Name() string { return "turns" }

// Register implements playhold.Module.
func (Module) Register(r *playhold.Registry) {
	playhold.State(r, key, func() *state { return &state{} })
	playhold.On(r, playerJoined)
	playhold.On(r, playerWon)
	playhold.View(r, view)
}

// playerJoined starts the game once the room is full. Whoever arrived first goes first.
func playerJoined(rc *playhold.RoomContext, _ playhold.PlayerJoined) {
	players := rc.Players()
	if len(players) != rules.Players {
		return
	}
	key.Get(rc).players = players
	rc.Emit(rules.GameStarted{Players: players})
}

func playerWon(rc *playhold.RoomContext, event rules.PlayerWon) {
	key.Get(rc).winner = event.Player
}

func view(rc *playhold.RoomContext) *pb.TurnsView {
	s := key.Get(rc)
	players := make([]string, 0, len(s.players))
	for _, player := range s.players {
		players = append(players, string(player))
	}
	return &pb.TurnsView{
		Started: len(s.players) > 0,
		Current: string(current(s)),
		Winner:  string(s.winner),
		Players: players,
	}
}

func current(s *state) playhold.PlayerID {
	if len(s.players) == 0 || s.winner != "" {
		return ""
	}
	return s.players[s.current]
}

// Keeper is what turns offers other modules.
type Keeper struct{}

// Current returns the player whose turn it is. It is empty unless the game is running.
func (Keeper) Current(rc *playhold.RoomContext) playhold.PlayerID {
	return current(key.Get(rc))
}

// Advance moves to the next player.
func (Keeper) Advance(rc *playhold.RoomContext) {
	s := key.Get(rc)
	s.current = (s.current + 1) % len(s.players)
}

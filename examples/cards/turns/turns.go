// Package turns owns whose turn it is, how long a turn can last, and whether the game is running.
package turns

import (
	"time"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
)

var key = playhold.NewKey[state]("turns")

type state struct {
	players   []playhold.PlayerID
	current   int
	winner    playhold.PlayerID
	turnTimer playhold.TimerID
}

// Module registers the turn order with a room.
type Module struct {
	// TurnTime is how long a player has for each turn.
	TurnTime time.Duration
	// FinishedRoomTime is how long a room stays open after its game ends, so players can see the result.
	FinishedRoomTime time.Duration
}

// Name implements playhold.Module.
func (Module) Name() string { return "turns" }

// Register implements playhold.Module.
func (m Module) Register(r *playhold.Registry) {
	playhold.State(r, key, func() *state { return &state{} })
	playhold.Snapshot(r, key, 1, save, load)
	playhold.On(r, m.playerJoined)
	playhold.On(r, m.playerWon)
	playhold.On(r, finishedGameExpired)
	playhold.View(r, m.view)
}

func save(s *state) *pb.TurnsState {
	players := make([]string, 0, len(s.players))
	for _, player := range s.players {
		players = append(players, string(player))
	}
	return &pb.TurnsState{
		Players:   players,
		Current:   uint32(s.current), //nolint:gosec // an index into a short list
		Winner:    string(s.winner),
		TurnTimer: uint64(s.turnTimer),
	}
}

func load(msg *pb.TurnsState) *state {
	players := make([]playhold.PlayerID, 0, len(msg.GetPlayers()))
	for _, player := range msg.GetPlayers() {
		players = append(players, playhold.PlayerID(player))
	}
	return &state{
		players:   players,
		current:   int(msg.GetCurrent()),
		winner:    playhold.PlayerID(msg.GetWinner()),
		turnTimer: playhold.TimerID(msg.GetTurnTimer()),
	}
}

// playerJoined starts the game once the room is full. Whoever arrived first goes first.
func (m Module) playerJoined(rc *playhold.RoomContext, _ playhold.PlayerJoined) {
	players := rc.Players()
	if len(players) != rules.Players {
		return
	}
	s := key.Get(rc)
	s.players = players
	startTurnTimer(rc, s, m.TurnTime)
	rc.Emit(rules.GameStarted{Players: players})
}

func (m Module) playerWon(rc *playhold.RoomContext, event rules.PlayerWon) {
	s := key.Get(rc)
	s.winner = event.Player
	rc.CancelTimer(s.turnTimer)
	rc.After(m.FinishedRoomTime, &pb.FinishedGameExpired{})
}

func finishedGameExpired(rc *playhold.RoomContext, _ *pb.FinishedGameExpired) {
	rc.CloseRoom()
}

func startTurnTimer(rc *playhold.RoomContext, s *state, turnTime time.Duration) {
	s.turnTimer = rc.After(turnTime, &pb.TurnExpired{Player: string(s.players[s.current])})
}

func (m Module) view(rc *playhold.RoomContext) *pb.TurnsView {
	s := key.Get(rc)
	players := make([]string, 0, len(s.players))
	for _, player := range s.players {
		players = append(players, string(player))
	}
	view := &pb.TurnsView{
		Started: len(s.players) > 0,
		Current: string(current(s)),
		Winner:  string(s.winner),
		Players: players,
	}
	if left, pending := rc.TimerLeft(s.turnTimer); pending && view.GetCurrent() != "" {
		view.TurnMillisLeft = left.Milliseconds()
		view.TurnMillisTotal = m.TurnTime.Milliseconds()
	}
	return view
}

func current(s *state) playhold.PlayerID {
	if len(s.players) == 0 || s.winner != "" {
		return ""
	}
	return s.players[s.current]
}

// Keeper is what turns offers other modules.
type Keeper struct {
	// TurnTime is how long a player has for each turn, the same as the module's.
	TurnTime time.Duration
}

// Current returns the player whose turn it is. It is empty unless the game is running.
func (Keeper) Current(rc *playhold.RoomContext) playhold.PlayerID {
	return current(key.Get(rc))
}

// Advance moves to the next player, who gets a full turn's time.
func (k Keeper) Advance(rc *playhold.RoomContext) {
	s := key.Get(rc)
	rc.CancelTimer(s.turnTimer)
	s.current = (s.current + 1) % len(s.players)
	startTurnTimer(rc, s, k.TurnTime)
}

// Package turns owns whose turn it is, how long a turn can last, and whether the game is running.
package turns

import (
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/rules"
)

const snapshotVersion2 = 2

var key = playhold.NewKey[state]("turns")

type state struct {
	players   []playhold.PlayerID
	current   int
	winner    playhold.PlayerID
	turnTimer playhold.TimerID
	away      []playhold.PlayerID
}

// Module registers the turn order with a room.
type Module struct {
	// TurnTime is how long a player has for each turn.
	TurnTime time.Duration
	// FinishedRoomTime is how long a room stays open after its game ends, so players can see the result.
	FinishedRoomTime time.Duration
	// OldestClientSchema is the oldest page the server still plays with.
	OldestClientSchema int
	// SnapshotVersion is the format saved games use. A real game would just change its code: this sample has a setting,
	// so a deploy that changes the format can be tried on a running game. Version 2 keeps the current player by ID, not
	// by position. The default is 1.
	SnapshotVersion int
}

// Name implements playhold.Module.
func (Module) Name() string { return "turns" }

// Register implements playhold.Module.
func (m Module) Register(r *playhold.Registry) {
	// The whole game's messages change together, so this one module speaks for all three.
	r.ClientSchema(int(pb.SchemaVersion_SCHEMA_VERSION_CURRENT), m.OldestClientSchema)
	playhold.State(r, key, func() *state { return &state{} })
	if m.SnapshotVersion >= snapshotVersion2 {
		playhold.Snapshot(r, key, snapshotVersion2, saveV2, loadV2)
		playhold.Migrate(r, key, 1, migrateToV2)
	} else {
		playhold.Snapshot(r, key, 1, save, load)
	}
	playhold.On(r, m.playerJoined)
	playhold.On(r, m.playerWon)
	playhold.On(r, finishedGameExpired)
	playhold.On(r, playerDisconnected)
	playhold.On(r, playerReturned)
	playhold.View(r, m.view)
}

func toStrings(players []playhold.PlayerID) []string {
	out := make([]string, 0, len(players))
	for _, player := range players {
		out = append(out, string(player))
	}
	return out
}

func toPlayers(ids []string) []playhold.PlayerID {
	out := make([]playhold.PlayerID, 0, len(ids))
	for _, id := range ids {
		out = append(out, playhold.PlayerID(id))
	}
	return out
}

func save(s *state) *pb.TurnsState {
	return &pb.TurnsState{
		Players:   toStrings(s.players),
		Current:   uint32(s.current), //nolint:gosec // an index into a short list
		Winner:    string(s.winner),
		TurnTimer: uint64(s.turnTimer),
		Away:      toStrings(s.away),
	}
}

func load(msg *pb.TurnsState) *state {
	return &state{
		players:   toPlayers(msg.GetPlayers()),
		current:   int(msg.GetCurrent()),
		winner:    playhold.PlayerID(msg.GetWinner()),
		turnTimer: playhold.TimerID(msg.GetTurnTimer()),
		away:      toPlayers(msg.GetAway()),
	}
}

// saveV2 is the same as save, with the current player by ID.
func saveV2(s *state) *pb.TurnsState {
	msg := save(s)
	msg.Current = 0
	if len(s.players) > 0 {
		msg.CurrentPlayer = string(s.players[s.current])
	}
	return msg
}

func loadV2(msg *pb.TurnsState) *state {
	s := load(msg)
	s.current = max(0, slices.Index(s.players, playhold.PlayerID(msg.GetCurrentPlayer())))
	return s
}

// migrateToV2 moves a game saved at version 1 to version 2.
func migrateToV2(old []byte) ([]byte, error) {
	msg := &pb.TurnsState{}
	if err := proto.Unmarshal(old, msg); err != nil {
		return nil, err
	}
	if players := msg.GetPlayers(); len(players) > 0 {
		msg.CurrentPlayer = players[msg.GetCurrent()]
	}
	msg.Current = 0
	return proto.Marshal(msg)
}

// playerDisconnected only shows that they've gone. The turn timer already keeps the game moving without them.
func playerDisconnected(rc *playhold.RoomContext, event playhold.PlayerDisconnected) {
	s := key.Get(rc)
	s.away = append(s.away, event.Player)
}

func playerReturned(rc *playhold.RoomContext, event playhold.PlayerReturned) {
	s := key.Get(rc)
	s.away = slices.DeleteFunc(s.away, func(player playhold.PlayerID) bool { return player == event.Player })
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
	view := &pb.TurnsView{
		Started: len(s.players) > 0,
		Current: string(current(s)),
		Winner:  string(s.winner),
		Players: toStrings(s.players),
		Away:    toStrings(s.away),
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

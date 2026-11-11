package playhold

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/roomtest"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

func init() {
	roomtest.Replay = replayRecording
}

// replayRecording plays a room's recording again against typ, segment by segment, and stops at the first turn that
// doesn't end as it did: another error, a panic or no panic, or another state. The room runs on the calling goroutine,
// with its clock at each turn's recorded time, and no timers of its own: a recorded timer firing runs that timer.
func replayRecording(next any, path string, logger *slog.Logger) (*roomtest.Report, error) {
	typ, ok := next.(*RoomType)
	if !ok || typ == nil {
		return nil, errNotARoomType
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	segments, err := readRecordingFile(path)
	if err != nil {
		return nil, err
	}
	report := &roomtest.Report{SchemaMatches: true}
	for i := range segments {
		segment := &segments[i]
		report.Build, report.Revision = segment.start.GetBuild(), segment.start.GetRevision()
		if !sameModules(typ, segment.start.GetModules()) {
			report.SchemaMatches = false
		}
		replayed, diverged := replaySegment(typ, segment, logger)
		report.Turns += replayed
		if diverged != nil {
			diverged.Segment = i
			report.Diverged = diverged
			return report, nil
		}
		if segment.gapAt >= 0 {
			report.Incomplete = true
		}
	}
	return report, nil
}

// sameModules reports whether typ's modules save at the versions the recording's did.
func sameModules(typ *RoomType, recorded []*wire.RecordedModule) bool {
	modules := typ.recordedModules()
	if len(modules) != len(recorded) {
		return false
	}
	for i := range modules {
		if modules[i].GetModule() != recorded[i].GetModule() || modules[i].GetVersion() != recorded[i].GetVersion() {
			return false
		}
	}
	return true
}

// replayer is one segment being replayed.
type replayer struct {
	room *Room
	now  time.Time
}

// discardSink is a replayed player's connection: replays build no views, so it never gets one. Every one is equal, so a
// leave finds the sink its seat used.
type discardSink struct{}

func (discardSink) SendState(*wire.RoomState) {}

// replaySegment replays a segment's turns, and returns how many it replayed, and the first that didn't end as recorded.
// A segment with a gap stops there.
func replaySegment(typ *RoomType, segment *recordedSegment, logger *slog.Logger) (int, *roomtest.Divergence) {
	start := segment.start
	p := &replayer{}
	var seed [32]byte
	copy(seed[:], start.GetSeed())
	cfg := Config{Logger: logger, StuckTimeout: defaultStuckTimeout, NewSeed: func() [32]byte { return seed },
		Now: func() time.Time { return p.now }}
	r := newRoom(RoomID(start.GetRoomId()), typ, &cfg, func(*Room) {})
	r.stable, r.replaying = true, true
	r.watchdog = time.AfterFunc(time.Hour, func() {})
	r.watchdog.Stop()
	p.room = r

	if err := p.begin(start); err != nil {
		return 0, &roomtest.Divergence{Reason: err.Error()}
	}
	if got := r.stateHash(); !bytes.Equal(got, start.GetHash()) {
		return 0, &roomtest.Divergence{Reason: "the room's state when it started running differs",
			WantHash: hex.EncodeToString(start.GetHash()), GotHash: hex.EncodeToString(got)}
	}
	turns := segment.turns
	if segment.gapAt >= 0 {
		turns = turns[:segment.gapAt] // records are missing after this, so nothing after it can be compared
	}
	for n, turn := range turns {
		if diverged := p.replay(turn); diverged != nil {
			diverged.Turn, diverged.Input = n+1, publicTurn(turn)
			return n, diverged
		}
		if turn.GetPanicked() {
			return n + 1, nil // the room closed, or was restored in a segment of its own
		}
	}
	return len(turns), nil
}

// begin brings the room to where its segment started: a new room runs Init at its recorded clock, and a restored one
// loads its recorded state. Then the turn that gave it its sessions runs.
func (p *replayer) begin(start *wire.RecordingSegment) error {
	r := p.room
	if restored := start.GetRestored(); len(restored) > 0 {
		if err := r.applySnapshot(restored, TimersKeepRunning); err != nil {
			return fmt.Errorf("the room's recorded state can't be restored: %w", err)
		}
	} else {
		p.now = time.Unix(0, start.GetInitUnixNanos())
		if err := r.runInit(); err != nil {
			return fmt.Errorf("the room's Init failed: %w", err)
		}
	}
	p.now = time.Unix(0, start.GetStartUnixNanos())
	if r.turn(r.sessionsInput(roomSessions{
		grace: time.Duration(start.GetGraceNanos()), inactive: time.Duration(start.GetInactiveNanos()),
	})) {
		return errors.New("the room's first turn panicked")
	}
	return nil
}

// replay runs one recorded turn and compares how it ended.
func (p *replayer) replay(turn *wire.RecordedTurn) *roomtest.Divergence {
	r := p.room
	p.now = time.Unix(0, turn.GetUnixNanos())
	in, why := p.input(turn)
	if why != "" {
		return &roomtest.Divergence{Reason: why}
	}
	var got error
	in.result = func(err error) { got = err }
	defer r.draining.Store(false)
	defer func(admit func() bool) { r.admit = admit }(r.admit)
	if seat := turn.GetSeat(); seat != nil {
		// The server refused it before asking the room, so it has to refuse it the same way now.
		switch seat.GetRefusedBy() {
		case refusedDraining:
			r.draining.Store(true)
		case refusedOverloaded:
			r.admit = func() bool { return false }
		}
	}
	panicked := r.turn(in)
	want := turn.GetError()
	var gotText string
	if got != nil {
		gotText = got.Error()
	}
	switch {
	case panicked != turn.GetPanicked() && panicked:
		return &roomtest.Divergence{Reason: "the turn panicked, and didn't when it was recorded", WantError: want}
	case panicked != turn.GetPanicked():
		return &roomtest.Divergence{Reason: "the turn panicked when it was recorded, and doesn't now", GotError: gotText}
	case panicked:
		return nil
	case gotText != want:
		return &roomtest.Divergence{Reason: "the turn's error differs", WantError: want, GotError: gotText}
	}
	if hash := r.stateHash(); !bytes.Equal(hash, turn.GetHash()) {
		return &roomtest.Divergence{Reason: "the room's state after the turn differs",
			WantHash: hex.EncodeToString(turn.GetHash()), GotHash: hex.EncodeToString(hash)}
	}
	return nil
}

// input rebuilds the turn's input, or says why it can't: a timer or grace period the room doesn't have pending.
func (p *replayer) input(turn *wire.RecordedTurn) (in input, why string) {
	r := p.room
	switch {
	case turn.GetMessage() != nil:
		msg := turn.GetMessage()
		return r.dispatchInput(PlayerID(msg.GetPlayer()), msg.GetModule(), msg.GetId(), msg.GetPayload(), nil), ""
	case turn.GetSeat() != nil:
		return r.seatInput(PlayerID(turn.GetSeat().GetPlayer()), discardSink{}, nil), ""
	case turn.GetLeave() != nil:
		return r.removeViewerInput(PlayerID(turn.GetLeave().GetPlayer()), discardSink{}), ""
	case turn.GetTimer() != nil:
		id := TimerID(turn.GetTimer().GetId())
		at := slices.IndexFunc(r.timers, func(t roomTimer) bool { return t.id == id })
		if at < 0 {
			return input{}, fmt.Sprintf("timer %d fired when it was recorded, but isn't pending now", id)
		}
		timer := r.timers[at]
		r.timers = slices.Delete(r.timers, at, at+1)
		return r.timerInput(timer), ""
	case turn.GetGrace() != nil:
		player := PlayerID(turn.GetGrace().GetPlayer())
		at := slices.IndexFunc(r.graces, func(g graceTimer) bool { return g.player == player })
		if at < 0 {
			return input{}, fmt.Sprintf("%s's grace period ended when it was recorded, but isn't running now", player)
		}
		r.graces = slices.Delete(r.graces, at, at+1)
		return r.graceInput(player), ""
	case turn.GetReserve() != nil:
		reserve := turn.GetReserve()
		players := make([]PlayerID, 0, len(reserve.GetPlayers()))
		for _, player := range reserve.GetPlayers() {
			players = append(players, PlayerID(player))
		}
		return r.reserveInput(players, int(reserve.GetMinPlayers())), ""
	case turn.GetReserveEnd() != nil:
		return r.reserveEndInput(), ""
	}
	return input{}, "the recording has a turn this version of playhold doesn't know"
}

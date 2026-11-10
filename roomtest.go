package playhold

import (
	"context"
	"errors"

	"github.com/ny4rl4th0t3p/playhold/internal/roomtest"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func init() {
	roomtest.New = newTestHarness
}

// testHarness runs one room for playholdtest as a server would, without the server: on a runtime of its own, with the
// server's timeouts, and, if the room type saves state, saving it after every change to a store in memory, so Restart
// can bring the room back.
type testHarness struct {
	cfg     *roomtest.Config
	seed    [32]byte // cfg.Seed, or a random one for a zero seed
	typ     *RoomType
	rt      *Runtime
	records store.Store
	writer  *snapshotWriter // nil if the room type saves nothing
	room    *Room
}

var errNotARoomType = errors.New("playhold: playholdtest needs a *playhold.RoomType")

func newTestHarness(cfg *roomtest.Config) (roomtest.Room, error) {
	typ, ok := cfg.Type.(*RoomType)
	if !ok || typ == nil {
		return nil, errNotARoomType
	}
	h := &testHarness{cfg: cfg, seed: cfg.Seed, records: memory.New()}
	if h.seed == ([32]byte{}) {
		h.seed = randomSeed()
	}
	h.start(typ)
	room, err := h.rt.createRoom(typ, "", h.writer, nil)
	if err != nil {
		_ = h.Close(context.Background()) // the room never started, so there's nothing to wait for
		return nil, err
	}
	h.run(room)
	return h, nil
}

// start makes a new runtime for rooms of typ, as a server starting would.
func (h *testHarness) start(typ *RoomType) {
	seed := h.seed
	h.typ = typ
	h.rt = NewRuntime(Config{Logger: h.cfg.Logger, NewSeed: func() [32]byte { return seed }})
	h.writer = nil
	if len(typ.snapshots) > 0 {
		h.writer = newSnapshotWriter(h.records, h.cfg.Logger)
	}
}

// run gives the room the server's sessions, as the lobby does.
func (h *testHarness) run(room *Room) {
	h.room = room
	room.startSessions(roomSessions{
		grace:    Timeout(h.cfg.Grace).orDefault(defaultDisconnectGrace).duration(),
		inactive: Timeout(h.cfg.Inactive).orDefault(defaultInactiveRoom).duration(),
	})
}

func (h *testHarness) Seat(player string, sink roomtest.Sink, result func(error)) error {
	return h.room.seat(PlayerID(player), sink, result)
}

func (h *testHarness) Leave(player string, sink roomtest.Sink) {
	h.room.removeViewer(PlayerID(player), sink)
}

func (h *testHarness) Send(player, module string, id uint32, payload []byte, result func(error)) error {
	return h.room.dispatch(PlayerID(player), module, id, payload, result)
}

func (h *testHarness) Do(fn func(rc any), result func(error)) error {
	return h.room.submit(input{result: result, run: func(rc *RoomContext) error {
		fn(rc)
		return nil
	}})
}

func (h *testHarness) Restart(next any) error {
	typ, ok := next.(*RoomType)
	switch {
	case !ok || typ == nil:
		return errNotARoomType
	case h.writer == nil:
		return errors.New("playhold: no module in the room type saves its state, so a restart would lose the room")
	}
	if err := errors.Join(h.typ.requireSnapshots(), typ.requireSnapshots()); err != nil {
		return err
	}
	ctx := context.Background()
	if err := h.Close(ctx); err != nil {
		return err
	}
	rooms, err := h.records.LoadRooms(ctx)
	switch {
	case err != nil:
		return err
	case len(rooms) == 0:
		return errors.New("playhold: the room had closed, so there's nothing to restart")
	}
	h.start(typ)
	room, err := h.rt.restoreRoom(typ, RoomID(rooms[0].ID), "", rooms[0].Data, h.writer, nil, TimersAfterRestart(h.cfg.Timers))
	if err != nil {
		return err
	}
	h.run(room)
	return nil
}

func (h *testHarness) Seed() [32]byte { return h.seed }

func (h *testHarness) Closed() (reason int, closed bool) {
	if !h.room.closed() {
		return 0, false
	}
	return int(h.room.reason), true
}

// Close stops the runtime, which saves the room as a shutdown does, and then writes what's waiting to be saved.
func (h *testHarness) Close(ctx context.Context) error {
	err := h.rt.Shutdown(ctx)
	if h.writer != nil {
		h.writer.close(ctx)
	}
	return err
}

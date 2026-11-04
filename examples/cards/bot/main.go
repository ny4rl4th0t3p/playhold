// Command bot puts load on the sample game: it plays many games at once over real connections, and reports how long
// the server takes to answer each move.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"

	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const (
	protocolVersion = 1
	module          = "hands"
	anySuit         = "hearts"

	readTimeout   = 30 * time.Second
	failurePause  = time.Second
	maxMoves      = 5000
	seatsPerGame  = 2
	runIDLength   = 6
	lowPercentile = 50
	topPercentile = 99
	maxReasons    = 5
)

var (
	errRefused    = errors.New("the server refused a frame")
	errRoomClosed = errors.New("the room closed")
	errTooLong    = errors.New("the game went on too long")
)

type config struct {
	url    string
	games  int
	think  time.Duration
	ramp   time.Duration
	report time.Duration
	idle   int
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.url, "url", "ws://localhost:8080/ws", "the server's WebSocket address")
	flag.IntVar(&cfg.games, "games", 100, "how many games to play at once, with two connections each")
	flag.DurationVar(&cfg.think, "think", time.Second, "how long a player waits before each move")
	flag.DurationVar(&cfg.ramp, "ramp", 10*time.Second, "how long to take starting all the games")
	flag.DurationVar(&cfg.report, "report", 5*time.Second, "how often to print a report")
	flag.IntVar(&cfg.idle, "idle", 0, "instead of playing, hold this many idle connections, to measure what one costs")
	duration := flag.Duration("duration", 0, "how long to run; 0 runs until interrupted")
	flag.Parse()

	if cfg.games <= 0 || cfg.report <= 0 {
		log.Fatal("-games and -report must be positive")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	if cfg.idle > 0 {
		holdIdle(ctx, cfg, os.Stdout)
		return
	}
	run(ctx, cfg, os.Stdout)
}

// holdIdle keeps connections open that have said hello and do nothing else, until ctx ends, and returns how many it
// managed. Comparing the server's memory with and without them shows what an idle connection costs.
func holdIdle(ctx context.Context, cfg *config, out io.Writer) (held int64) {
	var connected, reached, failed atomic.Int64
	var holders sync.WaitGroup
	for i := range cfg.idle {
		holders.Go(func() {
			delay := cfg.ramp * time.Duration(i) / time.Duration(cfg.idle)
			if !sleep(ctx, delay) {
				return
			}
			if err := holdOne(ctx, cfg, &connected, &reached); err != nil && ctx.Err() == nil {
				failed.Add(1)
			}
		})
	}

	finished := make(chan struct{})
	var reporter sync.WaitGroup
	reporter.Go(func() {
		ticker := time.NewTicker(cfg.report)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = fmt.Fprintf(out, "%s idle=%d failures=%d\n",
					time.Now().UTC().Format(time.TimeOnly), connected.Load(), failed.Load())
			case <-finished:
				return
			}
		}
	})

	holders.Wait()
	close(finished)
	reporter.Wait()
	return reached.Load()
}

// holdOne stays connected until ctx ends. Reading is what answers the server's pings.
func holdOne(ctx context.Context, cfg *config, connected, reached *atomic.Int64) error {
	ws, _, err := websocket.Dial(ctx, cfg.url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = ws.CloseNow() }()

	guest := &player{}
	if err := guest.send(ctx, ws, &wire.ClientFrame{Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: protocolVersion},
	}}); err != nil {
		return err
	}
	frame, err := readFrame(ctx, ws)
	if err != nil {
		return err
	}
	if frame.GetWelcome() == nil {
		return errRefused
	}

	reached.Add(1)
	connected.Add(1)
	defer connected.Add(-1)
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return err
		}
	}
}

// run plays until ctx ends, and returns the totals.
func run(ctx context.Context, cfg *config, out io.Writer) totals {
	rec := &recorder{}
	runID := rand.Text()[:runIDLength] // rooms stay open for a while, so a second run must not reuse their codes

	var tables sync.WaitGroup
	for table := range cfg.games {
		tables.Go(func() {
			delay := cfg.ramp * time.Duration(table) / time.Duration(cfg.games)
			if sleep(ctx, delay) {
				playTable(ctx, cfg, rec, fmt.Sprintf("bot-%s-%d", runID, table))
			}
		})
	}

	finished := make(chan struct{})
	var reporter sync.WaitGroup
	reporter.Go(func() {
		ticker := time.NewTicker(cfg.report)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rec.report(out, cfg.report)
			case <-finished:
				return
			}
		}
	})

	tables.Wait()
	close(finished)
	reporter.Wait()
	rec.report(out, cfg.report)
	return rec.snapshot()
}

// sleep reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// playTable plays one game after another with the same two players. Keeping their identities means the bot doesn't
// grow the server's list of guests, which would make memory readings look worse than a real game's.
func playTable(ctx context.Context, cfg *config, rec *recorder, table string) {
	players := [seatsPerGame]player{}
	for round := 0; ctx.Err() == nil; round++ {
		code := fmt.Sprintf("%s-%d", table, round)
		gameCtx, cancel := context.WithCancel(ctx)

		var seats sync.WaitGroup
		failed := make(chan struct{}, seatsPerGame)
		for i := range players {
			seats.Go(func() {
				if err := players[i].playGame(gameCtx, cfg, rec, code); err != nil {
					if gameCtx.Err() == nil { // otherwise the run is ending, or the other player already failed
						rec.failure(err)
					}
					failed <- struct{}{}
					cancel() // the other player would otherwise wait for a game that won't go on
				}
			})
		}
		seats.Wait()
		cancel()

		if len(failed) > 0 {
			sleep(ctx, failurePause)
		}
	}
}

// player is one connection's worth of a game.
type player struct {
	id     string
	secret string
	seq    uint64
}

func (p *player) playGame(ctx context.Context, cfg *config, rec *recorder, code string) error {
	ws, _, err := websocket.Dial(ctx, cfg.url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = ws.CloseNow() }()

	if err := p.send(ctx, ws, &wire.ClientFrame{Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: protocolVersion, PlayerId: p.id, Secret: p.secret},
	}}); err != nil {
		return err
	}

	var sentAt time.Time // when the move still waiting for an answer was sent
	for range maxMoves {
		frame, err := readFrame(ctx, ws)
		if err != nil {
			return err
		}
		switch body := frame.GetBody().(type) {
		case *wire.ServerFrame_Welcome:
			p.id = body.Welcome.GetPlayerId()
			if secret := body.Welcome.GetSecret(); secret != "" {
				p.secret = secret
			}
			err = p.send(ctx, ws, &wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: code}}})
		case *wire.ServerFrame_State:
			if !sentAt.IsZero() {
				rec.move(time.Since(sentAt))
				sentAt = time.Time{}
			}
			game, err := decodeState(body.State)
			if err != nil {
				return err
			}
			if winner := game.turns.GetWinner(); winner != "" {
				if winner == p.id {
					rec.game()
				}
				return nil
			}
			if game.turns.GetCurrent() != p.id {
				continue
			}
			if !sleep(ctx, cfg.think) {
				return ctx.Err()
			}
			sentAt = time.Now()
			if err := p.sendMove(ctx, ws, game.hands); err != nil {
				return err
			}
		case *wire.ServerFrame_Error:
			if body.Error.GetCode() == wire.ErrorCode_ERROR_CODE_UNAUTHORIZED {
				p.id, p.secret = "", "" // the server restarted, so start again as a new guest
			}
			return fmt.Errorf("%w: %s", errRefused, body.Error.GetMessage())
		case *wire.ServerFrame_Closed:
			return errRoomClosed
		}
		if err != nil {
			return err
		}
	}
	return errTooLong
}

func (p *player) sendMove(ctx context.Context, ws *websocket.Conn, hand *pb.HandsView) error {
	id, msg := chooseMove(hand)
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return p.send(ctx, ws, &wire.ClientFrame{Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: module, Id: uint32(id), Payload: payload}, //nolint:gosec // a small enum
	}})
}

func (p *player) send(ctx context.Context, ws *websocket.Conn, frame *wire.ClientFrame) error {
	p.seq++
	frame.Seq = p.seq
	data, err := proto.Marshal(frame)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageBinary, data)
}

func readFrame(ctx context.Context, ws *websocket.Conn) (*wire.ServerFrame, error) {
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	_, data, err := ws.Read(readCtx)
	if err != nil {
		return nil, err
	}
	frame := &wire.ServerFrame{}
	return frame, proto.Unmarshal(data, frame)
}

// gameView is the part of a state the bot needs to choose its move.
type gameView struct {
	turns *pb.TurnsView
	hands *pb.HandsView
}

func decodeState(state *wire.RoomState) (gameView, error) {
	game := gameView{turns: &pb.TurnsView{}, hands: &pb.HandsView{}}
	for _, view := range state.GetViews() {
		var err error
		switch view.GetModule() {
		case "turns":
			err = proto.Unmarshal(view.GetView(), game.turns)
		case module:
			err = proto.Unmarshal(view.GetView(), game.hands)
		}
		if err != nil {
			return game, err
		}
	}
	return game, nil
}

// chooseMove plays the first card it can, and otherwise draws, then passes. The server has already worked out what's
// possible, so the bot needs no rules of its own.
func chooseMove(hand *pb.HandsView) (id pb.HandsMessage, msg proto.Message) {
	switch {
	case len(hand.GetPlayable()) > 0:
		return pb.HandsMessage_HANDS_MESSAGE_PLAY, &pb.Play{Card: hand.GetPlayable()[0], ChosenSuit: anySuit}
	case hand.GetCanDraw():
		return pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}
	default:
		return pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{}
	}
}

// totals is what a whole run did.
type totals struct {
	games    int
	moves    int
	failures int
}

// recorder collects results from every player.
type recorder struct {
	mu        sync.Mutex
	totals    totals
	latencies []time.Duration // since the last report
	reasons   map[string]int  // since the last report: why games failed, and how many times
}

func (r *recorder) move(latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.moves++
	r.latencies = append(r.latencies, latency)
}

func (r *recorder) game() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.games++
}

func (r *recorder) failure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.failures++
	if r.reasons == nil {
		r.reasons = make(map[string]int)
	}
	r.reasons[err.Error()]++
}

func (r *recorder) snapshot() totals {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totals
}

// report prints the totals so far, and the move latency since the last report.
func (r *recorder) report(out io.Writer, interval time.Duration) {
	r.mu.Lock()
	sum := r.totals
	latencies := r.latencies
	reasons := r.reasons
	r.latencies, r.reasons = nil, nil
	r.mu.Unlock()

	slices.Sort(latencies)
	_, _ = fmt.Fprintf(out, "%s games=%d moves=%d failures=%d | moves/s=%.1f p%d=%s p%d=%s\n",
		time.Now().UTC().Format(time.TimeOnly),
		sum.games, sum.moves, sum.failures,
		float64(len(latencies))/interval.Seconds(),
		lowPercentile, percentile(latencies, lowPercentile),
		topPercentile, percentile(latencies, topPercentile))

	// Error texts can differ by connection, so only a few are printed, to keep a bad run readable.
	sorted := slices.Sorted(maps.Keys(reasons))
	for _, reason := range sorted[:min(len(sorted), maxReasons)] {
		_, _ = fmt.Fprintf(out, "failure: %dx %s\n", reasons[reason], reason)
	}
	if len(sorted) > maxReasons {
		_, _ = fmt.Fprintf(out, "failure: and %d other reasons\n", len(sorted)-maxReasons)
	}
}

// percentile reads from sorted latencies, and returns 0 if there are none.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[(len(sorted)-1)*p/100]
}

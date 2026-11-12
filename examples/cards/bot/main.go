// Command bot puts load on the sample game: it plays many games at once over real connections, and reports how long
// the server takes to answer each move. With -survive it instead plays one game through server restarts, and checks
// nothing was lost.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	mathrand "math/rand/v2" // the moves' timing; crypto/rand is for run IDs
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	sdk "github.com/ny4rl4th0t3p/playhold/sdk/go"
)

const (
	module  = "hands"
	anySuit = "hearts"

	readTimeout   = 30 * time.Second
	failurePause  = time.Second
	maxMoves      = 5000
	seatsPerGame  = 2
	runIDLength   = 6
	lowPercentile = 50
	topPercentile = 99
	nearTopMille  = 999
	maxReasons    = 5
	maxSamples    = 10_000_000 // about 80 MB, and a long run's percentiles won't move much beyond it
)

var errTooLong = errors.New("the game went on too long")

type config struct {
	url     string
	games   int
	think   time.Duration
	rate    float64
	spread  float64
	ramp    time.Duration
	warmup  time.Duration
	report  time.Duration
	idle    int
	survive int
	stall   time.Duration
	rejoin  bool
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.url, "url", "ws://localhost:8080/ws", "the server's WebSocket address")
	flag.IntVar(&cfg.games, "games", 100, "how many games to play at once, with two connections each")
	flag.DurationVar(&cfg.think, "think", time.Second, "how long a player waits before each move")
	flag.Float64Var(&cfg.rate, "rate", 0,
		"moves per second across all games, on a fixed schedule whether or not the server keeps up, instead of -think; "+
			"latency then counts from when each move was due")
	flag.Float64Var(&cfg.spread, "spread", 0,
		"with -rate, how irregular the moves are, from 0 to 1: each wait is the average one times a random factor from "+
			"1-spread to 1+spread, so 0.6 makes a player who moves every 5 s on average wait from 2 to 8 s")
	flag.DurationVar(&cfg.ramp, "ramp", 10*time.Second, "how long to take starting all the games")
	flag.DurationVar(&cfg.warmup, "warmup", 0, "how long to leave out of the final summary")
	flag.DurationVar(&cfg.report, "report", 5*time.Second, "how often to print a report")
	flag.IntVar(&cfg.idle, "idle", 0, "instead of playing, hold this many idle connections, to measure what one costs")
	flag.IntVar(&cfg.survive, "survive", 0, "instead of a load, play one game, which has to come through this many server interruptions")
	flag.DurationVar(&cfg.stall, "stall", time.Minute, "with -survive, how long to only draw and pass, so the game outlasts the interruptions")
	flag.BoolVar(&cfg.rejoin, "rejoin", false,
		"when a seat's connection drops, connect again as the same guest and join the same game, counting how long it "+
			"took; a seat that can't get back in, or whose cards changed, has lost its game")
	ca := flag.String("ca", "", "a PEM file of a certificate to trust, on top of the system's, for a server with a test certificate")
	duration := flag.Duration("duration", 0, "how long to run; 0 runs until interrupted")
	flag.Parse()

	if cfg.games <= 0 || cfg.report <= 0 || cfg.rate < 0 {
		log.Fatal("-games and -report must be positive, and -rate can't be negative")
	}
	if cfg.spread < 0 || cfg.spread > 1 {
		log.Fatal("-spread must be from 0 to 1")
	}
	if *ca != "" {
		if err := trustCA(*ca); err != nil {
			log.Fatal(err)
		}
	}
	if err := start(cfg, *duration); err != nil {
		log.Fatal(err)
	}
}

// trustCA makes every connection the bot opens trust the certificate in this PEM file, on top of the system's, so it can
// reach a server with a test certificate while still checking it. The SDK dials through Go's default HTTP client, so
// that's where it's set.
func trustCA(path string) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool() // no system pool here, so only the test certificate
	}
	if !pool.AppendCertsFromPEM(data) {
		return fmt.Errorf("-ca %s holds no PEM certificate", path)
	}
	defaults, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("-ca: Go's default HTTP transport isn't an *http.Transport")
	}
	transport := defaults.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	http.DefaultClient.Transport = transport
	return nil
}

func start(cfg *config, duration time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	switch {
	case cfg.survive > 0:
		return survive(ctx, cfg, os.Stdout)
	case cfg.idle > 0:
		holdIdle(ctx, cfg, os.Stdout)
	default:
		run(ctx, cfg, os.Stdout)
	}
	return nil
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
	client, err := sdk.Connect(ctx, &sdk.Options{URL: cfg.url, SchemaVersions: cards.ClientSchemaVersions()})
	if err != nil {
		return err
	}
	defer client.Close()

	reached.Add(1)
	connected.Add(1)
	defer connected.Add(-1)
	for {
		if _, err := client.Next(ctx); err != nil {
			return err
		}
	}
}

// run plays until ctx ends, and returns the totals.
func run(ctx context.Context, cfg *config, out io.Writer) totals {
	rec := &recorder{measureFrom: time.Now().Add(cfg.warmup), rejoin: cfg.rejoin}
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
	rec.summary(out)
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

// pacer decides when one table's next move goes out. A table only has one move to make at a time, so at a fixed rate a
// slow answer makes the next moves late, not fewer: each one's latency counts from when it was due, so a server that
// falls behind can't hide it by slowing the bot down.
//
// With a spread, each wait is the interval times a random factor from 1-spread to 1+spread, so players don't move like
// clockwork; the average stays the interval, and so the rate.
type pacer struct {
	mu       sync.Mutex
	think    time.Duration
	interval time.Duration
	spread   float64
	random   func() float64 // in [0, 1); math/rand's Float64, which tests replace
	due      time.Time
}

func newPacer(cfg *config) *pacer {
	p := &pacer{think: cfg.think, spread: cfg.spread, random: mathrand.Float64, due: time.Now()}
	if cfg.rate > 0 {
		p.interval = time.Duration(float64(cfg.games) / cfg.rate * float64(time.Second))
	}
	return p
}

// next is the wait until this table's next move.
func (p *pacer) next() time.Duration {
	if p.spread == 0 {
		return p.interval
	}
	return time.Duration(float64(p.interval) * (1 + p.spread*(2*p.random()-1)))
}

// wait returns once the next move should go out, with the time to count its latency from. It reports false if ctx ended.
func (p *pacer) wait(ctx context.Context) (due time.Time, ok bool) {
	if p.interval == 0 {
		if !sleep(ctx, p.think) {
			return time.Time{}, false
		}
		return time.Now(), true
	}
	p.mu.Lock()
	p.due = p.due.Add(p.next())
	due = p.due
	p.mu.Unlock()
	return due, sleep(ctx, time.Until(due))
}

// playTable plays one game after another with the same two players. Keeping their identities means the bot doesn't
// grow the server's list of guests, which would make memory readings look worse than a real game's.
func playTable(ctx context.Context, cfg *config, rec *recorder, table string) {
	players := [seatsPerGame]player{}
	for round := 0; ctx.Err() == nil; round++ {
		code := fmt.Sprintf("%s-%d", table, round)
		gameCtx, cancel := context.WithCancel(ctx)
		pace := newPacer(cfg) // connecting isn't part of any move's latency

		var seats sync.WaitGroup
		failed := make(chan struct{}, seatsPerGame)
		for i := range players {
			seats.Go(func() {
				if err := players[i].playGame(gameCtx, cfg, rec, pace, code); err != nil {
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

// player is one seat's identity, kept from game to game, and with -rejoin, where it stands in the game it's playing.
type player struct {
	identity sdk.Identity

	joined    bool      // this game's room has seated it
	droppedAt time.Time // when it lost its connection, until it's back in the game
	hand      []string  // its cards in the last view, to check after getting back in
}

// playGame plays one game to its end. With -rejoin, a dropped connection doesn't end it: the seat connects again and
// joins the same game, until it's back or can't be, which loses the game.
func (p *player) playGame(ctx context.Context, cfg *config, rec *recorder, pace *pacer, code string) error {
	p.joined, p.droppedAt, p.hand = false, time.Time{}, nil
	for {
		err := p.session(ctx, cfg, rec, pace, code)
		switch {
		case err == nil, !cfg.rejoin, ctx.Err() != nil:
			return err
		case !canRetry(err):
			if p.joined {
				rec.lost()
			}
			return err
		}
		if p.joined && p.droppedAt.IsZero() {
			p.droppedAt = time.Now()
		}
		sleep(ctx, reconnectPause)
	}
}

// session plays on one connection, until the game ends or the connection does.
func (p *player) session(ctx context.Context, cfg *config, rec *recorder, pace *pacer, code string) error {
	// #region connect
	client, err := sdk.Connect(ctx, &sdk.Options{URL: cfg.url, Identity: p.identity, SchemaVersions: cards.ClientSchemaVersions()})
	if err != nil {
		var refused *sdk.Error
		if errors.As(err, &refused) && refused.Code == sdk.ErrorUnauthorized {
			p.identity = sdk.Identity{} // the server restarted, so start again as a new guest
		}
		return err
	}
	defer client.Close()
	p.identity = client.Identity()

	if err := client.Join(ctx, code); err != nil {
		return err
	}
	// #endregion connect
	p.joined = true

	var sentAt time.Time // when the move still waiting for an answer was due
	for range maxMoves {
		game, err := nextGame(ctx, client)
		var refused *sdk.Error
		if cfg.rejoin && errors.As(err, &refused) && refused.Code != sdk.ErrorUnavailable {
			continue // a move crossed with a view after getting back in; the next view says what's true now
		}
		if err != nil {
			return err
		}
		if err := p.saw(rec, game.hands.GetCards()); err != nil {
			return err
		}
		if !sentAt.IsZero() {
			rec.move(time.Since(sentAt))
			sentAt = time.Time{}
		}
		if winner := game.turns.GetWinner(); winner != "" {
			if winner == p.identity.PlayerID {
				rec.game()
			}
			return nil
		}
		if game.turns.GetCurrent() != p.identity.PlayerID {
			continue
		}
		due, ok := pace.wait(ctx)
		if !ok {
			return ctx.Err()
		}
		sentAt = due
		id, msg := chooseMove(game.hands)
		if err := sendMove(ctx, client, id, msg); err != nil {
			return err
		}
	}
	return errTooLong
}

// saw checks the cards in the first view after getting back in, which can differ by one move: an answer cut off by the
// drop, or the very last move lost in a crash. More than that, and the game the seat came back to isn't the one it
// left.
func (p *player) saw(rec *recorder, hand []string) error {
	if !p.droppedAt.IsZero() {
		if different := handDifference(p.hand, hand); different > 1 {
			return fmt.Errorf("%w: had %d cards, and now %d", errCardsChanged, len(p.hand), len(hand))
		}
		rec.rejoined(time.Since(p.droppedAt))
		p.droppedAt = time.Time{}
	}
	p.hand = hand
	return nil
}

func sendMove(ctx context.Context, client *sdk.Client, id pb.HandsMessage, msg proto.Message) error {
	_, err := client.Send(ctx, module, uint32(id), msg) //nolint:gosec // a small enum
	return err
}

// nextGame waits for the player's next view of the game.
func nextGame(ctx context.Context, client *sdk.Client) (gameView, error) {
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	views, err := client.Next(readCtx)
	if err != nil {
		return gameView{}, err
	}
	return decodeViews(views)
}

// gameView is the part of a state the bot needs to choose its move.
type gameView struct {
	turns *pb.TurnsView
	hands *pb.HandsView
}

func decodeViews(views []sdk.ModuleView) (gameView, error) {
	game := gameView{turns: &pb.TurnsView{}, hands: &pb.HandsView{}}
	for _, view := range views {
		var err error
		switch view.Module {
		case "turns":
			err = proto.Unmarshal(view.View, game.turns)
		case module:
			err = proto.Unmarshal(view.View, game.hands)
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
	rejoined int // with -rejoin: seats back in their game after a dropped connection
	lost     int // with -rejoin: seats that couldn't get back into their game
}

// recorder collects results from every player.
type recorder struct {
	mu        sync.Mutex
	totals    totals
	latencies []time.Duration // since the last report
	reasons   map[string]int  // since the last report: why games failed, and how many times

	measureFrom time.Time       // what comes before this is warm-up
	measured    int             // moves since then
	samples     []time.Duration // their latencies, up to maxSamples

	rejoin      bool          // -rejoin, so the summary says how seats came back
	slowestBack time.Duration // the longest a seat took to get back into its game
}

func (r *recorder) rejoined(back time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.rejoined++
	r.slowestBack = max(r.slowestBack, back)
}

func (r *recorder) lost() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.lost++
}

func (r *recorder) move(latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals.moves++
	r.latencies = append(r.latencies, latency)
	if time.Now().Before(r.measureFrom) {
		return
	}
	r.measured++
	if len(r.samples) < maxSamples {
		r.samples = append(r.samples, latency)
	}
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

// summary prints the whole run after the warm-up, which is the line to compare between runs.
func (r *recorder) summary(out io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slices.Sort(r.samples)
	seconds := time.Since(r.measureFrom).Seconds()
	var slowest time.Duration
	if len(r.samples) > 0 {
		slowest = r.samples[len(r.samples)-1]
	}
	rejoins := ""
	if r.rejoin {
		rejoins = fmt.Sprintf(" rejoined=%d lost=%d slowest_back=%s", r.totals.rejoined, r.totals.lost, r.slowestBack)
	}
	_, _ = fmt.Fprintf(out, "summary moves=%d seconds=%.0f moves/s=%.1f failures=%d p50=%s p99=%s p99.9=%s max=%s%s\n",
		r.measured, seconds, float64(r.measured)/seconds, r.totals.failures,
		percentile(r.samples, lowPercentile), percentile(r.samples, topPercentile), perMille(r.samples, nearTopMille), slowest,
		rejoins)
}

// percentile reads from sorted latencies, and returns 0 if there are none.
func percentile(sorted []time.Duration, p int) time.Duration {
	return perMille(sorted, p*10)
}

// perMille is percentile in thousandths, for p99.9.
func perMille(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[(len(sorted)-1)*p/1000]
}

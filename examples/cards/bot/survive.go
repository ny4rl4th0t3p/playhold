package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
	sdk "github.com/ny4rl4th0t3p/playhold/sdk/go"
)

// reconnectPause is short, and doesn't grow, so the time to reconnect shows how long the server took to come back.
const reconnectPause = 200 * time.Millisecond

var (
	errCardsChanged = errors.New("a player's cards changed while they were disconnected")
	errBadEnding    = errors.New("the game didn't end properly")
	errTooFewDrops  = errors.New("the game ended before enough server interruptions")
)

// syncWriter lets both seats print.
type syncWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *syncWriter) printf(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = fmt.Fprintf(w.out, "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.TimeOnly)}, args...)...)
}

// survive plays one game on two seats, reconnecting whenever the server goes away, and checks that the game comes through
// it: each player gets their seat and cards back, and the game ends with a winner both agree on. It returns why not.
func survive(ctx context.Context, cfg *config, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log := &syncWriter{out: out}
	code := "survive-" + rand.Text()[:runIDLength]
	stallEnds := time.Now().Add(cfg.stall)

	var seats [seatsPerGame]*survivor
	var errs [seatsPerGame]error
	var players sync.WaitGroup
	for i := range seats {
		seats[i] = &survivor{cfg: cfg, log: log, name: fmt.Sprintf("seat %d", i), code: code, stallEnds: stallEnds}
		players.Go(func() {
			if errs[i] = seats[i].play(ctx); errs[i] != nil {
				cancel() // the other seat would otherwise wait for a game that won't go on
			}
		})
	}
	players.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return err
	}
	return checkEnding(cfg, log, seats[:])
}

func checkEnding(cfg *config, log *syncWriter, seats []*survivor) error {
	winner := seats[0].winner
	for _, seat := range seats {
		isWinner := seat.identity.PlayerID == winner
		if seat.winner != winner || isWinner != (len(seat.hand) == 0) {
			return fmt.Errorf("%w: %s says %q won, and holds %d cards", errBadEnding, seat.name, seat.winner, len(seat.hand))
		}
		if seat.reconnects < cfg.survive {
			return fmt.Errorf("%w: %s reconnected %d times, of %d", errTooFewDrops, seat.name, seat.reconnects, cfg.survive)
		}
		log.printf("%s: reconnected %d times, %d unseen moves, %d refused moves, slowest back in %s",
			seat.name, seat.reconnects, seat.unseenMoves, seat.refused, seat.slowestBack)
	}
	log.printf("survived: the game finished with a winner, and nobody lost a seat or a card")
	return nil
}

// survivor is one seat, across all its connections.
type survivor struct {
	cfg       *config
	log       *syncWriter
	name      string
	code      string
	stallEnds time.Time

	identity    sdk.Identity
	hand        []string
	seenHand    bool
	droppedAt   time.Time // when this seat lost its connection, until it's back in the game
	winner      string
	reconnects  int
	unseenMoves int
	refused     int
	slowestBack time.Duration
}

func (s *survivor) play(ctx context.Context) error {
	for {
		finished, err := s.session(ctx)
		switch {
		case finished:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case !canRetry(err):
			return fmt.Errorf("%s: %w", s.name, err)
		}
		if s.seenHand && s.droppedAt.IsZero() {
			s.droppedAt = time.Now()
			s.log.printf("%s: lost the connection: %v", s.name, err)
		}
		sleep(ctx, reconnectPause)
	}
}

// canRetry says whether an error is the server being away, and not something lost. A seat that can't get back in has
// lost its game, which is what this is here to find.
func canRetry(err error) bool {
	if errors.Is(err, sdk.ErrRoomClosed) || errors.Is(err, errCardsChanged) {
		return false
	}
	var refused *sdk.Error
	if errors.As(err, &refused) {
		return refused.Code == sdk.ErrorUnavailable
	}
	return true
}

// session plays on one connection, until the game ends or the connection does.
func (s *survivor) session(ctx context.Context) (finished bool, err error) {
	client, err := sdk.Connect(ctx, &sdk.Options{URL: s.cfg.url, Identity: s.identity, SchemaVersions: cards.ClientSchemaVersions()})
	if err != nil {
		return false, err
	}
	defer client.Close()
	s.identity = client.Identity()
	if err := client.Join(ctx, s.code); err != nil {
		return false, err
	}

	for {
		game, err := nextGame(ctx, client)
		var refused *sdk.Error
		if errors.As(err, &refused) && refused.Code != sdk.ErrorUnavailable {
			// A move can cross with a view after a reconnect. The view that follows says what's true now.
			s.refused++
			continue
		}
		if err != nil {
			return false, err
		}
		if err := s.saw(game.hands.GetCards()); err != nil {
			return false, err
		}
		if winner := game.turns.GetWinner(); winner != "" {
			s.winner = winner
			return true, nil
		}
		if game.turns.GetCurrent() != s.identity.PlayerID {
			continue
		}
		if !sleep(ctx, s.cfg.think) {
			return false, ctx.Err()
		}
		id, msg := s.chooseMove(game.hands)
		if err := sendMove(ctx, client, id, msg); err != nil {
			return false, err
		}
	}
}

// saw checks the cards in a view, if this is the first one since losing the connection. They can differ by one move, in
// either direction: a move's answer may have been cut off, or a crash may have lost the very last move.
func (s *survivor) saw(hand []string) error {
	if !s.droppedAt.IsZero() {
		back := time.Since(s.droppedAt)
		different := handDifference(s.hand, hand)
		if different > 1 {
			return fmt.Errorf("%w: had %v, and now %v", errCardsChanged, s.hand, hand)
		}
		s.unseenMoves += different
		s.reconnects++
		s.slowestBack = max(s.slowestBack, back)
		s.droppedAt = time.Time{}
		s.log.printf("%s: back in the game after %s, with %d cards", s.name, back.Round(time.Millisecond), len(hand))
	}
	s.hand, s.seenHand = hand, true
	return nil
}

// chooseMove keeps the game going until the interruptions are over, by never playing a card.
func (s *survivor) chooseMove(hand *pb.HandsView) (id pb.HandsMessage, msg proto.Message) {
	if time.Now().After(s.stallEnds) {
		return chooseMove(hand)
	}
	if hand.GetCanDraw() {
		return pb.HandsMessage_HANDS_MESSAGE_DRAW, &pb.Draw{}
	}
	return pb.HandsMessage_HANDS_MESSAGE_PASS, &pb.Pass{}
}

// handDifference counts the cards in only one of the two hands.
func handDifference(a, b []string) int {
	different := 0
	for _, card := range a {
		if !slices.Contains(b, card) {
			different++
		}
	}
	for _, card := range b {
		if !slices.Contains(a, card) {
			different++
		}
	}
	return different
}

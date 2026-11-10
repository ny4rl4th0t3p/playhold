package playhold

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
)

const (
	defaultMatchEvery = time.Second
	defaultReserveFor = 30 * time.Second

	// What a client may send with a Queue: enough for a game's choices, not a place to store data.
	maxQueueProperties  = 16
	maxQueuePropertyLen = 256
)

// Matchmaking turns on matching players with each other: a player queues, the game's Matcher forms groups from those
// waiting, and each group gets a new room, with its seats kept for them. playhold does the waiting, the rooms and the
// timeouts; the game decides who plays whom.
type Matchmaking struct {
	// Matcher forms the groups. Nil makes groups of RoomConfig.MaxPlayers, in the order players queued.
	Matcher Matcher
	// Describe returns what the game knows about a player, such as their level or region, read from the game's own
	// data. It runs once, when the player queues, on their connection's goroutine, so waiting on a database is fine
	// there; its result is Candidate.Info. An error refuses the Queue with UNAVAILABLE. Nil leaves Info nil.
	Describe func(ctx context.Context, player PlayerID) (any, error)
	// Every is how often the matcher runs, over everyone waiting, and so the longest a player waits for their first
	// chance at a match. Nothing else starts it, so it bounds what an expensive matcher costs. The default is 1 second.
	Every time.Duration
	// ReserveFor is how long a new room keeps its seats for the matched players. If one hasn't joined by then, the room
	// closes as CloseMatchIncomplete, and those who joined can queue again. The default is 30 seconds.
	ReserveFor time.Duration
}

// Candidate is a player waiting for a game. It gains fields as playhold learns more about players.
type Candidate struct {
	Player PlayerID
	// Properties is what the player's client sent, such as a mode they chose. A client can send anything, so use it for
	// choices, never for what has to be trusted, such as a level: that comes from Info.
	Properties map[string]string
	// Info is what Matchmaking.Describe returned for the player.
	Info any
	// Waiting is how long the player has waited.
	Waiting time.Duration
}

// Matcher forms groups from the players waiting, in the order they queued, and returns them. Each group gets a new
// room. Players left out keep waiting. A group may have at most RoomConfig.MaxPlayers players, each waiting and in one
// group only; otherwise the whole result is dropped and logged, and nobody is matched until the next run.
type Matcher func(waiting []Candidate) [][]PlayerID

// waiter is a player in the queue, with how to tell their connection about a match.
type waiter struct {
	candidate Candidate
	since     time.Time
	conn      *transport.Conn // the connection that queued, which alone can leave
}

// matchmaker keeps the queue and runs the matcher.
type matchmaker struct {
	cfg        Matchmaking
	lobby      *lobby
	maxPlayers int
	logger     *slog.Logger
	metrics    *metrics

	mu      sync.Mutex
	waiting []*waiter // in the order they queued
}

// newMatchmaker returns the server's matchmaker, or nil if matchmaking is off. checkConfig has already made sure a
// default matcher has a group size.
func newMatchmaker(cfg *Matchmaking, l *lobby, m *metrics, logger *slog.Logger) *matchmaker {
	if cfg == nil {
		return nil
	}
	mm := &matchmaker{cfg: *cfg, lobby: l, maxPlayers: l.typ.cfg.MaxPlayers, logger: logger, metrics: m}
	if mm.cfg.Matcher == nil {
		mm.cfg.Matcher = groupsInOrder(mm.maxPlayers)
	}
	if mm.cfg.Every <= 0 {
		mm.cfg.Every = defaultMatchEvery
	}
	if mm.cfg.ReserveFor <= 0 {
		mm.cfg.ReserveFor = defaultReserveFor
	}
	return mm
}

// groupsInOrder is the default matcher: full groups of size, in the order players queued.
func groupsInOrder(size int) Matcher {
	return func(waiting []Candidate) [][]PlayerID {
		var groups [][]PlayerID
		for start := 0; start+size <= len(waiting); start += size {
			group := make([]PlayerID, 0, size)
			for i := start; i < start+size; i++ {
				group = append(group, waiting[i].Player)
			}
			groups = append(groups, group)
		}
		return groups
	}
}

// describe asks the game about a player, or returns nil without Describe.
func (mm *matchmaker) describe(player PlayerID) (any, error) {
	if mm.cfg.Describe == nil {
		return nil, nil // no Describe means no Info, which isn't an error
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	return mm.cfg.Describe(ctx, player)
}

// add puts a player in the queue, replacing any place they had. The next run of the matcher sees them.
func (mm *matchmaker) add(w *waiter) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.waiting = slices.DeleteFunc(mm.waiting, func(old *waiter) bool { return old.candidate.Player == w.candidate.Player })
	mm.waiting = append(mm.waiting, w)
}

// remove takes the player out of the queue, if conn is the connection that queued them: a newer connection's place
// stays.
func (mm *matchmaker) remove(player PlayerID, conn *transport.Conn) {
	if mm == nil {
		return
	}
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.waiting = slices.DeleteFunc(mm.waiting, func(w *waiter) bool { return w.candidate.Player == player && w.conn == conn })
}

// waitingCount is how many players are in the queue.
func (mm *matchmaker) waitingCount() int {
	if mm == nil {
		return 0
	}
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return len(mm.waiting)
}

// run runs the matcher every cfg.Every, until ctx ends. A run that takes longer just delays the next: a ticker drops
// the ticks it missed, so runs never pile up or overlap.
func (mm *matchmaker) run(ctx context.Context) {
	if mm == nil {
		return
	}
	ticker := time.NewTicker(mm.cfg.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		mm.match()
	}
}

// match runs the matcher once, and gives each group it forms a room.
func (mm *matchmaker) match() {
	candidates := mm.candidates()
	if len(candidates) == 0 {
		return
	}
	groups, ok := mm.runMatcher(candidates)
	if !ok {
		return
	}
	if err := checkGroups(groups, candidates, mm.maxPlayers); err != nil {
		mm.logger.Error("the matcher's groups were dropped; nobody was matched this time", "error", err)
		return
	}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		taken := mm.take(group)
		if taken == nil {
			continue // someone left the queue while the matcher ran, so the others wait for the next run
		}
		code, err := mm.lobby.match(group, mm.cfg.ReserveFor)
		if err != nil {
			mm.putBack(taken)
			mm.logger.Warn("no room for a matched group, so they keep waiting", "players", len(group), "error", err)
			return
		}
		mm.metrics.matched()
		for _, w := range taken {
			w.conn.SendMatched(code)
		}
	}
}

// candidates returns who's waiting, in the order they queued, with how long each has waited.
func (mm *matchmaker) candidates() []Candidate {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	now := time.Now()
	candidates := make([]Candidate, 0, len(mm.waiting))
	for _, w := range mm.waiting {
		c := w.candidate
		c.Waiting = now.Sub(w.since)
		candidates = append(candidates, c)
	}
	return candidates
}

// runMatcher calls the game's matcher. A panic in it is the game's bug: it's logged, and nobody is matched this run.
func (mm *matchmaker) runMatcher(candidates []Candidate) (groups [][]PlayerID, ok bool) {
	defer func() {
		if v := recover(); v != nil {
			mm.logger.Error("the matcher panicked; nobody was matched this time", "panic", v, "stack", string(debug.Stack()))
			ok = false
		}
	}()
	return mm.cfg.Matcher(candidates), true
}

// take removes a group from the queue and returns its places, or nil, removing nobody, if any of them has left.
func (mm *matchmaker) take(group []PlayerID) []*waiter {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	taken := make([]*waiter, 0, len(group))
	for _, player := range group {
		at := slices.IndexFunc(mm.waiting, func(w *waiter) bool { return w.candidate.Player == player })
		if at < 0 {
			return nil
		}
		taken = append(taken, mm.waiting[at])
	}
	mm.waiting = slices.DeleteFunc(mm.waiting, func(w *waiter) bool { return slices.Contains(taken, w) })
	return taken
}

// putBack returns a group to the queue, in their places by when they queued, if they haven't queued again meanwhile.
func (mm *matchmaker) putBack(taken []*waiter) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	for _, w := range taken {
		if !slices.ContainsFunc(mm.waiting, func(other *waiter) bool { return other.candidate.Player == w.candidate.Player }) {
			mm.waiting = append(mm.waiting, w)
		}
	}
	slices.SortStableFunc(mm.waiting, func(a, b *waiter) int { return a.since.Compare(b.since) })
}

var errBadGroups = errors.New("playhold: the matcher returned groups it can't")

// checkGroups makes sure every group is one a room can take: players who are waiting, each in one group, no more than
// maxPlayers of them, if the room type has a limit.
func checkGroups(groups [][]PlayerID, waiting []Candidate, maxPlayers int) error {
	present := make(map[PlayerID]bool, len(waiting))
	for i := range waiting {
		present[waiting[i].Player] = true
	}
	seen := make(map[PlayerID]bool)
	for _, group := range groups {
		if maxPlayers > 0 && len(group) > maxPlayers {
			return fmt.Errorf("%w: a group of %d players, over the room's %d seats", errBadGroups, len(group), maxPlayers)
		}
		for _, player := range group {
			switch {
			case !present[player]:
				return fmt.Errorf("%w: a player who isn't waiting", errBadGroups)
			case seen[player]:
				return fmt.Errorf("%w: a player in two groups, or twice in one", errBadGroups)
			}
			seen[player] = true
		}
	}
	return nil
}

package playhold

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"sync"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/store"
)

// guests remembers guest identities, through the store, and which connection each one is using, in memory.
type guests struct {
	records store.Store

	mu    sync.Mutex
	conns map[PlayerID]*transport.Conn
}

func newGuests(records store.Store) *guests {
	return &guests{records: records, conns: make(map[PlayerID]*transport.Conn)}
}

// create saves and returns a new guest's ID and secret. The two are separate, so a player's ID can be shown to others.
// Only a hash of the secret is stored, so reading the database doesn't reveal it.
func (g *guests) create(ctx context.Context) (player PlayerID, secret string, err error) {
	player, secret = PlayerID(rand.Text()), rand.Text()
	if err := g.records.SaveGuest(ctx, string(player), hashSecret(secret)); err != nil {
		return "", "", err
	}
	return player, secret, nil
}

// verify reports whether the secret belongs to the player. An error means it couldn't be checked.
func (g *guests) verify(ctx context.Context, player PlayerID, secret string) (bool, error) {
	want, err := g.records.GuestSecretHash(ctx, string(player))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return subtle.ConstantTimeCompare(hashSecret(secret), want) == 1, nil
}

func hashSecret(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// attach makes c the player's connection and returns the one it replaces, if any.
func (g *guests) attach(player PlayerID, c *transport.Conn) *transport.Conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	old := g.conns[player]
	g.conns[player] = c
	return old
}

// detach forgets c, unless a newer connection has already replaced it.
func (g *guests) detach(player PlayerID, c *transport.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[player] == c {
		delete(g.conns, player)
	}
}

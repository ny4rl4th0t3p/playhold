package playhold

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"sync"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
)

// guests remembers guest identities and which connection each one is using. Everything is in memory, so a
// restart forgets every guest.
type guests struct {
	mu      sync.Mutex
	secrets map[PlayerID][sha256.Size]byte // hashed, so a memory dump doesn't reveal them
	conns   map[PlayerID]*transport.Conn
}

func newGuests() *guests {
	return &guests{
		secrets: make(map[PlayerID][sha256.Size]byte),
		conns:   make(map[PlayerID]*transport.Conn),
	}
}

// create returns a new guest's ID and secret. The two are separate, so a player's ID can be shown to others.
func (g *guests) create() (player PlayerID, secret string) {
	player, secret = PlayerID(rand.Text()), rand.Text()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.secrets[player] = sha256.Sum256([]byte(secret))
	return player, secret
}

// verify reports whether the secret belongs to the player.
func (g *guests) verify(player PlayerID, secret string) bool {
	g.mu.Lock()
	want, ok := g.secrets[player]
	g.mu.Unlock()
	if !ok {
		return false
	}
	got := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
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

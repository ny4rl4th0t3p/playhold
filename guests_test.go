package playhold

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
)

func TestGuestSecrets(t *testing.T) {
	g := newGuests()
	player, secret := g.create()
	other, otherSecret := g.create()

	assert.NotEqual(t, player, other)
	assert.NotEqual(t, string(player), secret)

	tests := []struct {
		name   string
		player PlayerID
		secret string
		want   bool
	}{
		{name: "right secret", player: player, secret: secret, want: true},
		{name: "wrong secret", player: player, secret: "wrong", want: false},
		{name: "another guest's secret", player: player, secret: otherSecret, want: false},
		{name: "empty secret", player: player, secret: "", want: false},
		{name: "unknown player", player: "nobody", secret: secret, want: false},
		{name: "empty player", player: "", secret: secret, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, g.verify(tt.player, tt.secret))
		})
	}
}

func TestGuestConnections(t *testing.T) {
	g := newGuests()
	first, second := &transport.Conn{}, &transport.Conn{}

	assert.Nil(t, g.attach("alice", first))
	assert.Same(t, first, g.attach("alice", second))

	// The replaced connection ending must not forget the newer one.
	g.detach("alice", first)
	assert.Same(t, second, g.attach("alice", second))

	g.detach("alice", second)
	assert.Nil(t, g.attach("alice", first))
}

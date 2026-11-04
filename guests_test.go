package playhold

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

var errStoreDown = errors.New("store is down")

// downStore is a Store whose database can't be reached.
type downStore struct{}

func (downStore) SaveGuest(_ context.Context, _ string, _ []byte) error { return errStoreDown }

func (downStore) GuestSecretHash(_ context.Context, _ string) ([]byte, error) {
	return nil, errStoreDown
}

func TestGuestSecrets(t *testing.T) {
	g := newGuests(memory.New())
	player, secret, err := g.create(t.Context())
	require.NoError(t, err)
	other, otherSecret, err := g.create(t.Context())
	require.NoError(t, err)

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
			got, err := g.verify(t.Context(), tt.player, tt.secret)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStoreOnlySeesAHashOfTheSecret(t *testing.T) {
	records := memory.New()
	g := newGuests(records)

	player, secret, err := g.create(t.Context())
	require.NoError(t, err)

	stored, err := records.GuestSecretHash(t.Context(), string(player))
	require.NoError(t, err)
	assert.NotContains(t, string(stored), secret)
	assert.Equal(t, hashSecret(secret), stored)
}

func TestGuestsSurviveANewServerWithTheSameStore(t *testing.T) {
	records := memory.New()
	player, secret, err := newGuests(records).create(t.Context())
	require.NoError(t, err)

	got, err := newGuests(records).verify(t.Context(), player, secret)

	require.NoError(t, err)
	assert.True(t, got)
}

func TestGuestsReportAStoreThatIsDown(t *testing.T) {
	g := newGuests(downStore{})

	_, _, err := g.create(t.Context())
	require.ErrorIs(t, err, errStoreDown)

	_, err = g.verify(t.Context(), "alice", "secret")
	require.ErrorIs(t, err, errStoreDown)
}

func TestGuestConnections(t *testing.T) {
	g := newGuests(memory.New())
	first, second := &transport.Conn{}, &transport.Conn{}

	assert.Nil(t, g.attach("alice", first))
	assert.Same(t, first, g.attach("alice", second))

	// The replaced connection ending must not forget the newer one.
	g.detach("alice", first)
	assert.Same(t, second, g.attach("alice", second))

	g.detach("alice", second)
	assert.Nil(t, g.attach("alice", first))
}

package playhold

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// savedGuest saves a guest last seen this long ago.
func savedGuest(t *testing.T, records store.Store, player string, ago time.Duration) {
	t.Helper()
	require.NoError(t, records.SaveGuest(t.Context(), player, hashSecret("secret")))
	require.NoError(t, records.TouchGuest(t.Context(), player, time.Now().Add(-ago)))
}

func guestKnown(t *testing.T, records store.Store, player string) bool {
	_, err := records.GuestSecretHash(t.Context(), player)
	return err == nil
}

func TestGuestsUnseenForTooLongAreDeleted(t *testing.T) {
	records := memory.New()
	savedGuest(t, records, "gone", 100*24*time.Hour)
	savedGuest(t, records, "recent", 24*time.Hour)

	server, _ := startObservedServer(t, &ServerConfig{Store: records})

	require.Eventually(t, func() bool { return !guestKnown(t, records, "gone") }, testTimeout, 10*time.Millisecond,
		"deleted as the server starts, by the 90 days default")
	assert.True(t, guestKnown(t, records, "recent"))
	assert.InDelta(t, 1.0, testutil.ToFloat64(server.metrics.guestsDeleted), 0)
}

func TestGuestExpiryOffKeepsEveryone(t *testing.T) {
	records := memory.New()
	savedGuest(t, records, "gone", 100*24*time.Hour)

	startObservedServer(t, &ServerConfig{Store: records, GuestExpiry: Timeout{Off: true}})
	time.Sleep(50 * time.Millisecond) // long enough for a deletion that shouldn't happen

	assert.True(t, guestKnown(t, records, "gone"))
}

func TestAReturningGuestIsSeenAgain(t *testing.T) {
	records := memory.New()
	_, url := startObservedServer(t, &ServerConfig{Store: records, GuestExpiry: Timeout{Off: true}})
	_, welcome := newGuest(t, url)
	longAgo := time.Now().Add(-100 * 24 * time.Hour)
	require.NoError(t, records.TouchGuest(t.Context(), welcome.GetPlayerId(), longAgo))

	returningGuest(t, url, welcome)

	deleted, err := records.DeleteGuestsUnseenSince(t.Context(), longAgo.Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, deleted, "the Hello counts as being seen now")
	assert.True(t, guestKnown(t, records, welcome.GetPlayerId()))
}

package postgres

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/storetest"
	"github.com/ny4rl4th0t3p/playhold/store"
)

// testDatabaseEnv names the database for these tests, which `make integration` provides. Each test gets its own schema
// in it, so they don't interfere.
const testDatabaseEnv = "PLAYHOLD_TEST_POSTGRES"

// newSchema returns the URL of a new, empty schema, which is dropped when the test ends.
func newSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv(testDatabaseEnv)
	if base == "" {
		t.Skipf("run `make integration` for the tests that need PostgreSQL (%s isn't set)", testDatabaseEnv)
	}

	admin, err := pgxpool.New(t.Context(), base)
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	schema := "test_" + strings.ToLower(rand.Text())
	_, err = admin.Exec(t.Context(), `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA `+schema+` CASCADE`)
		assert.NoError(t, err)
	})

	parsed, err := url.Parse(base)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func openStore(t *testing.T, databaseURL string) *Store {
	t.Helper()
	opened, err := Open(t.Context(), databaseURL)
	require.NoError(t, err)
	t.Cleanup(opened.Close)
	return opened
}

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return openStore(t, newSchema(t)) })
}

func TestGuestsSurviveReopening(t *testing.T) {
	databaseURL := newSchema(t)
	first := openStore(t, databaseURL)
	require.NoError(t, first.SaveGuest(t.Context(), "alice", []byte("hash")))
	first.Close()

	got, err := openStore(t, databaseURL).GuestSecretHash(t.Context(), "alice")

	require.NoError(t, err)
	assert.Equal(t, []byte("hash"), got)
}

func TestServersCanStartTogether(t *testing.T) {
	databaseURL := newSchema(t)

	var starting sync.WaitGroup
	for range 4 {
		starting.Go(func() {
			opened, err := Open(t.Context(), databaseURL)
			if assert.NoError(t, err) {
				opened.Close()
			}
		})
	}
	starting.Wait()
}

func TestOpenReportsAnUnreachableDatabase(t *testing.T) {
	_, err := Open(t.Context(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")

	require.Error(t, err)
}

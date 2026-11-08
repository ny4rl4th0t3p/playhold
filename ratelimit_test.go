package playhold

import (
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// never refills within a test, so a burst is all a test gets.
const never = time.Hour

func TestRateLimitDefaults(t *testing.T) {
	def := RateLimit{Every: time.Second, Burst: 10}
	tests := []struct {
		name  string
		limit RateLimit
		want  RateLimit
	}{
		{name: "zero uses the default", want: def},
		{name: "set fields are kept", limit: RateLimit{Every: time.Minute, Burst: 3}, want: RateLimit{Every: time.Minute, Burst: 3}},
		{name: "a missing field comes from the default", limit: RateLimit{Burst: 3}, want: RateLimit{Every: time.Second, Burst: 3}},
		{name: "off stays off", limit: RateLimit{Off: true}, want: RateLimit{Off: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.limit.orDefault(def))
		})
	}

	assert.Nil(t, RateLimit{Off: true}.limiter())
	assert.Nil(t, newPerIP(RateLimit{Off: true}))
	assert.True(t, allow(nil), "a layer that's off allows everything")
	assert.True(t, (*perIP)(nil).allow("203.0.113.5"))
}

func TestPerIPRefillsAndForgetsQuietAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := newPerIP(RateLimit{Every: time.Second, Burst: 2})

		assert.True(t, limits.allow("a"))
		assert.True(t, limits.allow("a"))
		assert.False(t, limits.allow("a"), "the burst is spent")
		assert.True(t, limits.allow("b"), "each address has its own limit")

		time.Sleep(time.Second)
		assert.True(t, limits.allow("a"), "one more after one interval")
		assert.False(t, limits.allow("a"))

		time.Sleep(sweepEvery)
		limits.allow("c")
		limits.mu.Lock()
		defer limits.mu.Unlock()
		assert.Equal(t, []string{"c"}, mapKeys(limits.limiters), "quiet addresses were back at a full burst, so they're forgotten")
	})
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestConnectRateRefusesNewConnectionsFromOneAddress(t *testing.T) {
	url := startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{}), ConnectRate: RateLimit{Every: never, Burst: 2}})

	dialServer(t, url)
	dialServer(t, url)
	_, resp, err := websocket.Dial(gatewayContext(t), url, nil) //nolint:bodyclose // Dial's docs say the body needs no closing

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestGuestRateRefusesNewGuestsButNotReturningOnes(t *testing.T) {
	url := startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{}), GuestRate: RateLimit{Every: never, Burst: 1}})
	_, first := newGuest(t, url)

	ws := dialServer(t, url)
	sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
	refused := readServerFrame(t, ws).GetError()

	require.NotNil(t, refused)
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_RATE_LIMITED, refused.GetCode())
	assert.Equal(t, uint64(1), refused.GetSeq())
	returningGuest(t, url, first) // coming back creates nothing, so it isn't limited
}

func TestMessageRateRefusesFramesOverTheLimit(t *testing.T) {
	url := startServerWith(t, &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{Modules: []Module{viewOfSaved, savedModule}}),
		MessageRate: RateLimit{Every: never, Burst: 3}, // Hello, Join and one message
	})
	alice, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, alice).GetState())
	sendFrame(t, alice, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	require.Equal(t, "5", savedView(t, readServerFrame(t, alice)))

	sendFrame(t, alice, messageFrame(t, 4, "saved", msgAdd, &addMsg{Value: 5}))
	refused := readServerFrame(t, alice).GetError()

	require.NotNil(t, refused)
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_RATE_LIMITED, refused.GetCode())
	assert.Equal(t, uint64(4), refused.GetSeq())
	bob, _ := seatedGuest(t, url) // a connection of its own, with its own limit
	assert.Equal(t, "5", savedView(t, readServerFrame(t, bob)), "the refused message never reached the room")
}

func TestModuleRateLimitsOneMessage(t *testing.T) {
	limited := registerFunc{name: "saved", register: func(r *Registry) {
		savedModule.register(r)
		r.RateLimit(msgAdd, RateLimit{Every: never, Burst: 1})
	}}
	url := startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{Modules: []Module{viewOfSaved, limited}})})
	alice, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, alice).GetState())
	sendFrame(t, alice, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	require.Equal(t, "5", savedView(t, readServerFrame(t, alice)))

	sendFrame(t, alice, messageFrame(t, 4, "saved", msgAdd, &addMsg{Value: 5}))
	refused := readServerFrame(t, alice).GetError()

	require.NotNil(t, refused)
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_RATE_LIMITED, refused.GetCode())
	assert.Equal(t, uint64(4), refused.GetSeq())
	bob, _ := seatedGuest(t, url)
	assert.Equal(t, "5", savedView(t, readServerFrame(t, bob)), "the refused message never reached the room")
}

func TestModuleRateLimitMistakes(t *testing.T) {
	handled := func(r *Registry) { Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { return nil }) }
	tests := []struct {
		name     string
		register func(r *Registry)
		wantErr  string
	}{
		{
			name: "no burst",
			register: func(r *Registry) {
				handled(r)
				r.RateLimit(msgAdd, RateLimit{Every: time.Second})
			},
			wantErr: "positive Every and Burst",
		},
		{
			name: "off, which only the server's layers can be",
			register: func(r *Registry) {
				handled(r)
				r.RateLimit(msgAdd, RateLimit{Off: true})
			},
			wantErr: "positive Every and Burst",
		},
		{
			name: "declared twice",
			register: func(r *Registry) {
				handled(r)
				r.RateLimit(msgAdd, RateLimit{Every: time.Second, Burst: 1})
				r.RateLimit(msgAdd, RateLimit{Every: time.Second, Burst: 1})
			},
			wantErr: "declared twice",
		},
		{
			name:     "no handler",
			register: func(r *Registry) { r.RateLimit(msgAdd, RateLimit{Every: time.Second, Burst: 1}) },
			wantErr:  "has no handler",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRoomType(RoomConfig{Name: "test", Modules: []Module{registerFunc{name: "m", register: tt.register}}})

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

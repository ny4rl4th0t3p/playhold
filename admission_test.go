package playhold

import (
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

func TestAdmissionDefaults(t *testing.T) {
	assert.Equal(t, Admission{Memory: 0.9, SchedP99: 20 * time.Millisecond, Queue: 0.5}, Admission{}.orDefault())
	assert.Equal(t, Admission{Memory: 0.5, SchedP99: 20 * time.Millisecond, Queue: 0.5}, Admission{Memory: 0.5}.orDefault())
	assert.Equal(t, Admission{Off: true}, Admission{Off: true}.orDefault())
	assert.Nil(t, newAdmission(Admission{Off: true}, nil, slog.New(slog.DiscardHandler)))
	assert.True(t, (*admission)(nil).allow(), "admission control off takes everything")
}

func TestP99FromTwoReadings(t *testing.T) {
	// Real boundaries from the runtime's layout (runtime/histogram.go): power-of-2 nanosecond ranges, each split in 4.
	// This is a slice of them around the 20 ms default, between the underflow and overflow buckets.
	buckets := []float64{
		math.Inf(-1),
		0.016777216, // 2^24 ns
		0.020971520, // 2^24 + 2^22 ns
		0.025165824,
		0.029360128,
		0.033554432, // 2^25 ns
		math.Inf(1),
	}
	at := func(i int) time.Duration { return time.Duration(buckets[i] * float64(time.Second)) }
	tests := []struct {
		name        string
		earlier     []uint64
		later       []uint64
		want        time.Duration
		wantSamples uint64
	}{
		{name: "nothing since", earlier: []uint64{5, 5, 5, 5, 5, 5}, later: []uint64{5, 5, 5, 5, 5, 5}},
		{
			name: "a p99 of 17-21 ms reads as 16.8 ms, under the 20 ms default", earlier: make([]uint64, 6),
			later: []uint64{0, 200, 0, 0, 0, 0}, want: at(1), wantSamples: 200,
		},
		{
			name: "a p99 of 21-25 ms reads as 21 ms, over it", earlier: make([]uint64, 6),
			later: []uint64{0, 98, 2, 0, 0, 0}, want: at(2), wantSamples: 100,
		},
		{
			name: "one slow wait in a hundred isn't the p99", earlier: make([]uint64, 6),
			later: []uint64{0, 99, 0, 0, 1, 0}, want: at(1), wantSamples: 100,
		},
		{
			name: "only the waits since the earlier reading count", earlier: []uint64{0, 0, 0, 50, 0, 0},
			later: []uint64{0, 100, 0, 50, 0, 0}, want: at(1), wantSamples: 100,
		},
		{
			name: "the open last bucket reads as its bottom", earlier: make([]uint64, 6),
			later: []uint64{0, 0, 0, 0, 0, 10}, want: at(5), wantSamples: 10,
		},
		{
			name: "the underflow bucket reads as zero", earlier: make([]uint64, 6),
			later: []uint64{10, 0, 0, 0, 0, 0}, wantSamples: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			later := schedHistogram{counts: tt.later, buckets: buckets}
			earlier := schedHistogram{counts: tt.earlier, buckets: buckets}

			got, samples := later.p99Since(earlier)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantSamples, samples)
		})
	}
}

func TestAdmissionEntersOnAnySignalAndLeavesAfterStayingWellUnder(t *testing.T) {
	calm := load{memory: 0.5, schedP99: time.Millisecond, schedSamples: 1000, queue: 0.1}
	tests := []struct {
		name string
		over func(l *load)
	}{
		{name: "memory", over: func(l *load) { l.memory = 0.95 }},
		{name: "waits", over: func(l *load) { l.schedP99 = 30 * time.Millisecond }},
		{name: "queues", over: func(l *load) { l.queue = 0.6 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAdmission(Admission{}.orDefault(), nil, slog.New(slog.DiscardHandler))
			a.check(calm)
			require.True(t, a.allow())

			busy := calm
			tt.over(&busy)
			a.check(busy)
			require.False(t, a.allow(), "one signal over is enough")

			for range admissionCalm - 1 {
				a.check(calm)
			}
			require.False(t, a.allow(), "not yet calm for long enough")
			a.check(busy)
			for range admissionCalm - 1 {
				a.check(calm)
			}
			require.False(t, a.allow(), "going over again starts the count over")

			a.check(calm)
			assert.True(t, a.allow(), "calm for long enough")
		})
	}
}

func TestAdmissionNeedsSignalsWellUnderToCountAsCalm(t *testing.T) {
	a := newAdmission(Admission{}.orDefault(), nil, slog.New(slog.DiscardHandler))
	a.check(load{memory: 0.95})
	for range 3 * admissionCalm {
		a.check(load{memory: 0.85}) // under 0.9, but not under 90% of it
	}
	assert.False(t, a.allow())
}

func TestSlowWaitsFromTooFewSamplesDontCount(t *testing.T) {
	a := newAdmission(Admission{}.orDefault(), nil, slog.New(slog.DiscardHandler))
	a.check(load{schedP99: time.Second, schedSamples: minSchedSamples - 1})
	assert.True(t, a.allow())
}

// fakeProcess stands in for the runtime's measurements.
type fakeProcess struct{ memory atomic.Uint64 } // math.Float64bits

func (f *fakeProcess) read() (float64, schedHistogram) {
	return math.Float64frombits(f.memory.Load()), schedHistogram{}
}

func (f *fakeProcess) setMemory(share float64) { f.memory.Store(math.Float64bits(share)) }

func TestOverloadedServerRefusesNewGuestsRoomsAndSeatsButNotPlayersComingBack(t *testing.T) {
	fake := &fakeProcess{}
	saved := readProcess
	readProcess = fake
	t.Cleanup(func() { readProcess = saved })
	server, err := NewServer(newTestRuntime(t), &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{}),
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
		Development: true,
	})
	require.NoError(t, err)
	url := startServerFor(t, server)
	alice, aliceWelcome := seatedGuest(t, url)
	bob, _ := newGuest(t, url)
	_ = alice.CloseNow()

	fake.setMemory(1) // so the sampler agrees with what this test sets
	server.admission.overloaded.Store(true)

	tests := []struct {
		name  string
		frame *wire.ClientFrame
		ws    func() *websocket.Conn
		want  wire.ErrorCode
	}{
		{
			name:  "a new guest, told to try again later",
			frame: helloFrame(protocolVersion, "", ""),
			ws:    func() *websocket.Conn { return dialServer(t, url) },
			want:  wire.ErrorCode_ERROR_CODE_UNAVAILABLE,
		},
		{
			name:  "a new room",
			frame: joinFrame("another-room"),
			ws:    func() *websocket.Conn { return bob },
			want:  wire.ErrorCode_ERROR_CODE_SERVER_FULL,
		},
		{
			name:  "a new seat in a room that exists",
			frame: joinFrame(testCode),
			ws:    func() *websocket.Conn { return bob },
			want:  wire.ErrorCode_ERROR_CODE_SERVER_FULL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := tt.ws()
			sendFrame(t, ws, tt.frame)

			refused := readServerFrame(t, ws).GetError()

			require.NotNil(t, refused)
			assert.Equal(t, tt.want, refused.GetCode())
			assert.Contains(t, refused.GetMessage(), "too busy")
		})
	}

	back := returningGuest(t, url, aliceWelcome)
	joinRoom(t, back, testCode) // a player with a seat always gets back in
}

// startServerFor serves an existing server and returns its URL.
func startServerFor(t *testing.T, server *Server) string {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

package playhold

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func newRecorder(t *testing.T) (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return recorder, provider
}

// ended returns the spans with this name that have ended. It doesn't fail the test itself, so require.Eventually can
// call it.
func ended(recorder *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var spans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			spans = append(spans, span)
		}
	}
	return spans
}

func attr(span sdktrace.ReadOnlySpan, key attribute.Key) string {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value.String()
		}
	}
	return ""
}

func TestTurnsAreTraced(t *testing.T) {
	recorder, provider := newRecorder(t)
	_, url := startObservedServer(t, &ServerConfig{TracerProvider: provider})
	ws, welcome := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 1}))
	require.NotNil(t, readServerFrame(t, ws).GetState())
	sendFrame(t, ws, messageFrame(t, 4, "saved", msgRefuseAfterAdding, &addMsg{Value: 1}))
	require.NotNil(t, readServerFrame(t, ws).GetError())
	require.NotNil(t, readServerFrame(t, ws).GetState())

	// A turn's span ends after its answer and views are out, so the client can be a little ahead of it.
	require.Eventually(t, func() bool { return len(ended(recorder, "playhold.turn")) >= 3 }, testTimeout, 10*time.Millisecond)
	results := map[string]string{}
	kinds := map[string]int{}
	for _, span := range ended(recorder, "playhold.turn") {
		kinds[attr(span, attrKind)]++
		assert.Equal(t, "match", attr(span, attrRoomType))
		assert.NotEmpty(t, attr(span, attrRoom))
		if attr(span, attrKind) != "message" {
			continue
		}
		assert.Equal(t, welcome.GetPlayerId(), attr(span, attrPlayer))
		assert.Equal(t, "saved", attr(span, attrModule))
		results[attr(span, attrMessage)] = attr(span, attrResult)
		assert.Equal(t, codes.Unset, span.Status().Code, "a refused move is the game working")
	}
	assert.Equal(t, map[string]string{
		strconv.FormatUint(uint64(msgAdd), 10): "ok", strconv.FormatUint(uint64(msgRefuseAfterAdding), 10): "rejected",
	}, results)
	assert.Positive(t, kinds["other"], "the join")
}

func TestAPanickingTurnIsAnError(t *testing.T) {
	recorder, provider := newRecorder(t)
	_, url := startObservedServer(t, &ServerConfig{TracerProvider: provider, Store: memory.New()})
	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgPanic, &addMsg{}))
	require.NotNil(t, readServerFrame(t, ws).GetError())

	require.Eventually(t, func() bool {
		for _, span := range ended(recorder, "playhold.turn") {
			if span.Status().Code == codes.Error {
				return true
			}
		}
		return false
	}, testTimeout, 10*time.Millisecond)
}

func TestTheRestoreAndStoreCallsAreTraced(t *testing.T) {
	recorder, provider := newRecorder(t)
	_, url := startObservedServer(t, &ServerConfig{TracerProvider: provider, Store: memory.New()})

	restores := ended(recorder, "playhold.restore")
	require.Len(t, restores, 1, "it ends before NewServer returns")
	loads := ended(recorder, "playhold.store.LoadRooms")
	require.Len(t, loads, 1)
	assert.Equal(t, restores[0].SpanContext().SpanID(), loads[0].Parent().SpanID(), "loading is part of the restore")
	assert.Equal(t, "0", attr(loads[0], attrRooms))

	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())
	assert.Len(t, ended(recorder, "playhold.store.SaveGuest"), 1, "the new guest")
	require.Eventually(t, func() bool { return len(ended(recorder, "playhold.store.WriteRooms")) > 0 },
		testTimeout, 10*time.Millisecond, "the room is saved in the background")
}

func TestAStoreCallIsAnErrorOnlyIfItFailed(t *testing.T) {
	tests := []struct {
		name    string
		records store.Store
		call    func(ctx context.Context, s store.Store) error
		want    codes.Code
	}{
		{
			name:    "a guest saved",
			records: memory.New(),
			call:    func(ctx context.Context, s store.Store) error { return s.SaveGuest(ctx, "alice", []byte("hash")) },
			want:    codes.Unset,
		},
		{
			name:    "a guest not found",
			records: memory.New(),
			call: func(ctx context.Context, s store.Store) error {
				_, err := s.GuestSecretHash(ctx, "nobody")
				return err
			},
			want: codes.Unset,
		},
		{
			name:    "the database down",
			records: downStore{},
			call:    func(ctx context.Context, s store.Store) error { return s.SaveGuest(ctx, "alice", []byte("hash")) },
			want:    codes.Error,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder, provider := newRecorder(t)
			traced := traceStore(tt.records, newTracer(provider, slog.New(slog.DiscardHandler)))

			_ = tt.call(t.Context(), traced)

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, tt.want, spans[0].Status().Code)
		})
	}
}

func TestWithoutAProviderNothingIsTraced(t *testing.T) {
	records := memory.New()

	assert.Same(t, records, traceStore(records, newTracer(nil, slog.New(slog.DiscardHandler))), "the store isn't wrapped")
	assert.Nil(t, (&Room{}).startTurnSpan(&input{module: "m"}), "no span, and no attributes built for one")
}

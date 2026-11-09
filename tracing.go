package playhold

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// tracerName names playhold's tracer, as OpenTelemetry asks: after the package.
const tracerName = "github.com/ny4rl4th0t3p/playhold"

// The attributes playhold puts on its spans.
const (
	attrRoom     = attribute.Key("playhold.room.id")
	attrRoomType = attribute.Key("playhold.room.type")
	attrPlayer   = attribute.Key("playhold.player.id")
	attrModule   = attribute.Key("playhold.message.module")
	attrMessage  = attribute.Key("playhold.message.id")
	attrEvent    = attribute.Key("playhold.timer.event")
	attrKind     = attribute.Key("playhold.turn.kind")
	attrResult   = attribute.Key("playhold.message.result")
	attrRooms    = attribute.Key("playhold.rooms")
	attrDeletes  = attribute.Key("playhold.rooms.deleted")
)

// newTracer returns playhold's tracer from the game's provider, or nil without one. Everything traced checks for nil
// first, so a game that traces nothing doesn't even build the spans' attributes. It logs when tracing is on, since that
// costs CPU at the rate the provider samples.
func newTracer(provider trace.TracerProvider, logger *slog.Logger) trace.Tracer {
	if provider == nil {
		return nil
	}
	logger.Info("tracing on; every turn and store call is offered to the tracer provider, which samples them")
	return provider.Tracer(tracerName)
}

// startTurnSpan starts the span of the turn about to run, or returns nil without a tracer. A room has no context of its
// own, so each turn starts a trace.
func (r *Room) startTurnSpan(in *input) trace.Span {
	if r.tracer == nil {
		return nil
	}
	attrs := []attribute.KeyValue{
		attrRoom.String(string(r.id)), attrRoomType.String(r.typ.cfg.Name), attrKind.String(turnKindNames[kindOf(in)]),
	}
	if in.player != "" && !r.hidePlayers {
		attrs = append(attrs, attrPlayer.String(string(in.player)))
	}
	if in.module != "" {
		attrs = append(attrs, attrModule.String(in.module), attrMessage.Int64(int64(in.id)))
	}
	if in.timerEvent != "" {
		attrs = append(attrs, attrEvent.String(in.timerEvent))
	}
	_, span := r.tracer.Start(context.Background(), "playhold.turn", trace.WithAttributes(attrs...))
	return span
}

// endTurnSpan ends a turn's span. A handler refusing a message is the game working, so only a panic is an error. A turn
// the watchdog gave up on never ends its span, since its goroutine never gets here, or only much later.
func endTurnSpan(span trace.Span, module string, err error, panicked bool) {
	if span == nil {
		return
	}
	switch {
	case panicked:
		span.SetStatus(codes.Error, "the turn panicked")
	case module != "":
		span.SetAttributes(attrResult.String(resultNames[resultOf(err)]))
	}
	span.End()
}

// tracedStore gives each call to the store a span. Ping isn't traced: health checks would bury everything else.
type tracedStore struct {
	store.Store
	tracer trace.Tracer
}

// traceStore wraps records in spans, or returns it as it is without a tracer.
func traceStore(records store.Store, tracer trace.Tracer) store.Store {
	if tracer == nil {
		return records
	}
	return tracedStore{Store: records, tracer: tracer}
}

func (s tracedStore) start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return s.tracer.Start(ctx, "playhold.store."+name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// endStoreSpan ends a store call's span, as an error if it failed, and returns err. Not finding a record isn't a failure.
func endStoreSpan(span trace.Span, err error) error {
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
	return err
}

// SaveGuest implements store.Store.
func (s tracedStore) SaveGuest(ctx context.Context, player string, secretHash []byte) error {
	ctx, span := s.start(ctx, "SaveGuest")
	return endStoreSpan(span, s.Store.SaveGuest(ctx, player, secretHash))
}

// GuestSecretHash implements store.Store.
func (s tracedStore) GuestSecretHash(ctx context.Context, player string) ([]byte, error) {
	ctx, span := s.start(ctx, "GuestSecretHash")
	hash, err := s.Store.GuestSecretHash(ctx, player)
	return hash, endStoreSpan(span, err)
}

// WriteRooms implements store.Store.
func (s tracedStore) WriteRooms(ctx context.Context, saves []store.RoomRecord, deletes []string) error {
	ctx, span := s.start(ctx, "WriteRooms", attrRooms.Int(len(saves)), attrDeletes.Int(len(deletes)))
	return endStoreSpan(span, s.Store.WriteRooms(ctx, saves, deletes))
}

// LoadRooms implements store.Store.
func (s tracedStore) LoadRooms(ctx context.Context) ([]store.RoomRecord, error) {
	ctx, span := s.start(ctx, "LoadRooms")
	rooms, err := s.Store.LoadRooms(ctx)
	span.SetAttributes(attrRooms.Int(len(rooms)))
	return rooms, endStoreSpan(span, err)
}

// SaveIncident implements store.Store.
func (s tracedStore) SaveIncident(ctx context.Context, incident *store.Incident) error {
	ctx, span := s.start(ctx, "SaveIncident")
	return endStoreSpan(span, s.Store.SaveIncident(ctx, incident))
}

// LoadIncidents implements store.Store.
func (s tracedStore) LoadIncidents(ctx context.Context) ([]store.Incident, error) {
	ctx, span := s.start(ctx, "LoadIncidents")
	incidents, err := s.Store.LoadIncidents(ctx)
	return incidents, endStoreSpan(span, err)
}

// DeleteIncidents implements store.Store.
func (s tracedStore) DeleteIncidents(ctx context.Context, keepBuild string, before time.Time) error {
	ctx, span := s.start(ctx, "DeleteIncidents")
	return endStoreSpan(span, s.Store.DeleteIncidents(ctx, keepBuild, before))
}

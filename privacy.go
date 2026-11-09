package playhold

import (
	"context"
	"log/slog"
)

// playerKey is the attribute every log line about a player uses.
const playerKey = "player"

// withoutPlayers is a log handler that drops the player attribute, for ServerConfig.HidePlayerIDs. Filtering here, not at
// each call, means no log line can forget to.
type withoutPlayers struct {
	slog.Handler
}

func (h withoutPlayers) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler's signature
	kept := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != playerKey {
			kept.AddAttrs(a)
		}
		return true
	})
	return h.Handler.Handle(ctx, kept)
}

func (h withoutPlayers) WithAttrs(attrs []slog.Attr) slog.Handler {
	kept := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Key != playerKey {
			kept = append(kept, a)
		}
	}
	return withoutPlayers{h.Handler.WithAttrs(kept)}
}

func (h withoutPlayers) WithGroup(name string) slog.Handler {
	return withoutPlayers{h.Handler.WithGroup(name)}
}

// hidePlayers returns logger leaving players' IDs out.
func hidePlayers(logger *slog.Logger) *slog.Logger {
	return slog.New(withoutPlayers{logger.Handler()})
}

// serverLogger returns the server's logger: ServerConfig.Logger, or the runtime's. With HidePlayerIDs, it leaves players'
// IDs out, and so do the runtime's rooms and traces.
func serverLogger(cfg *ServerConfig, rt *Runtime) *slog.Logger {
	logger := cfg.Logger
	if logger == nil {
		logger = rt.logger
	}
	if !cfg.HidePlayerIDs {
		return logger
	}
	rt.hidePlayerIDs()
	logger = hidePlayers(logger)
	logger.Info("player IDs left out of logs and traces")
	return logger
}

// Package postgres is a store.Store on PostgreSQL.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// How a server waits while another one is migrating: check every second, for up to five minutes.
const (
	migrationLockPeriodSeconds = 1
	migrationLockAttempts      = 300
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is a store.Store on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// Open connects to the database at url and brings its tables up to date.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("postgres: connecting: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close disconnects from the database.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}
	return nil
}

// SaveGuest implements store.Store.
func (s *Store) SaveGuest(ctx context.Context, player string, secretHash []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO guests (player_id, secret_hash, last_seen) VALUES ($1, $2, now())
		ON CONFLICT (player_id) DO UPDATE SET secret_hash = excluded.secret_hash, last_seen = excluded.last_seen`,
		player, secretHash)
	if err != nil {
		return fmt.Errorf("postgres: saving guest: %w", err)
	}
	return nil
}

// GuestSecretHash implements store.Store.
func (s *Store) GuestSecretHash(ctx context.Context, player string) ([]byte, error) {
	var hash []byte
	err := s.pool.QueryRow(ctx, `SELECT secret_hash FROM guests WHERE player_id = $1`, player).Scan(&hash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, store.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("postgres: reading guest: %w", err)
	}
	return hash, nil
}

// TouchGuest implements store.Store.
func (s *Store) TouchGuest(ctx context.Context, player string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE guests SET last_seen = $2 WHERE player_id = $1`, player, at); err != nil {
		return fmt.Errorf("postgres: touching guest: %w", err)
	}
	return nil
}

// DeleteGuestsUnseenSince implements store.Store.
func (s *Store) DeleteGuestsUnseenSince(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM guests WHERE last_seen < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: deleting unseen guests: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// WriteRooms implements store.Store.
func (s *Store) WriteRooms(ctx context.Context, saves []store.RoomRecord, deletes []string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if len(saves) > 0 {
			batch := &pgx.Batch{}
			for i := range saves {
				batch.Queue(`
					INSERT INTO rooms (room_id, code, room_type, data) VALUES ($1, $2, $3, $4)
					ON CONFLICT (room_id) DO UPDATE SET
						code = excluded.code, room_type = excluded.room_type, data = excluded.data, updated_at = now()`,
					saves[i].ID, saves[i].Code, saves[i].RoomType, saves[i].Data)
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
		}
		if len(deletes) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM rooms WHERE room_id = ANY($1)`, deletes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: writing rooms: %w", err)
	}
	return nil
}

// LoadRooms implements store.Store.
func (s *Store) LoadRooms(ctx context.Context) ([]store.RoomRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT room_id, code, room_type, data FROM rooms`)
	if err != nil {
		return nil, fmt.Errorf("postgres: loading rooms: %w", err)
	}
	rooms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.RoomRecord, error) {
		var record store.RoomRecord
		err := row.Scan(&record.ID, &record.Code, &record.RoomType, &record.Data)
		return record, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: loading rooms: %w", err)
	}
	return rooms, nil
}

// SaveIncident implements store.Store.
func (s *Store) SaveIncident(ctx context.Context, incident *store.Incident) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO incidents (build, kind, room_id, player_id, module, message_id, event, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		incident.Build, incident.Kind, incident.Room, incident.Player, incident.Module, int64(incident.MessageID),
		incident.Event, incident.At)
	if err != nil {
		return fmt.Errorf("postgres: saving incident: %w", err)
	}
	return nil
}

// LoadIncidents implements store.Store.
func (s *Store) LoadIncidents(ctx context.Context) ([]store.Incident, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT build, kind, room_id, player_id, module, message_id, event, at FROM incidents ORDER BY at, id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: loading incidents: %w", err)
	}
	incidents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Incident, error) {
		var incident store.Incident
		var messageID int64
		err := row.Scan(&incident.Build, &incident.Kind, &incident.Room, &incident.Player, &incident.Module, &messageID,
			&incident.Event, &incident.At)
		incident.MessageID = uint32(messageID) //nolint:gosec // saved from a uint32
		incident.At = incident.At.UTC()
		return incident, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: loading incidents: %w", err)
	}
	return incidents, nil
}

// DeleteIncidents implements store.Store.
func (s *Store) DeleteIncidents(ctx context.Context, keepBuild string, before time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM incidents WHERE build <> $1 OR at < $2`, keepBuild, before); err != nil {
		return fmt.Errorf("postgres: deleting incidents: %w", err)
	}
	return nil
}

// migrate applies the migrations the database hasn't had yet. The lock lets servers start together safely.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("postgres: reading migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(migrationLockPeriodSeconds, migrationLockAttempts))
	if err != nil {
		return fmt.Errorf("postgres: preparing migration lock: %w", err)
	}
	// Closing the provider closes this database handle, but not the pool underneath it.
	provider, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(pool), files, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("postgres: preparing migrations: %w", err)
	}
	defer func() { _ = provider.Close() }()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("postgres: migrating: %w", err)
	}
	return nil
}

// Package postgres is a store.Store on PostgreSQL.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

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
		INSERT INTO guests (player_id, secret_hash) VALUES ($1, $2)
		ON CONFLICT (player_id) DO UPDATE SET secret_hash = excluded.secret_hash`,
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

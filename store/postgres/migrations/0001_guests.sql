-- +goose Up
CREATE TABLE guests (
    player_id   text PRIMARY KEY,
    secret_hash bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
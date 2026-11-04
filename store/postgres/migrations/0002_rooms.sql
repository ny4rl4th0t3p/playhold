-- +goose Up
CREATE TABLE rooms (
    room_id    text PRIMARY KEY,
    code       text NOT NULL,
    room_type  text NOT NULL,
    data       bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
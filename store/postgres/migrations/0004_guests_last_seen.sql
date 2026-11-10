-- +goose Up
-- Guests seen before this migration count as seen now, so none expires the day it runs.
ALTER TABLE guests ADD COLUMN last_seen timestamptz NOT NULL DEFAULT now();
CREATE INDEX guests_last_seen ON guests (last_seen);
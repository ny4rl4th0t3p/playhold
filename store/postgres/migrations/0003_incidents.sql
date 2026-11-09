-- +goose Up
CREATE TABLE incidents
(
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build      text        NOT NULL,
    kind       text        NOT NULL,
    room_id    text        NOT NULL,
    player_id  text        NOT NULL,
    module     text        NOT NULL,
    message_id bigint      NOT NULL,
    event      text        NOT NULL,
    at         timestamptz NOT NULL
);
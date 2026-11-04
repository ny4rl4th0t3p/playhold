package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/examples/cards/pb"
)

func TestBotPlaysWholeGamesAgainstARealServer(t *testing.T) {
	const games = 4

	rt := playhold.NewRuntime(playhold.Config{Logger: slog.New(slog.DiscardHandler)})
	typ, err := cards.NewRoomType()
	require.NoError(t, err)
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{RoomType: typ, MemoryLimit: -1, MaxConnsPerIP: 1024})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, rt.Shutdown(ctx))
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out bytes.Buffer

	got := run(ctx, &config{
		url:    "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/",
		games:  games,
		report: time.Hour,
	}, &out)

	assert.Positive(t, got.games, "no game finished")
	assert.Positive(t, got.moves)
	assert.Zero(t, got.failures)
	assert.Contains(t, out.String(), "games=")
}

func TestBotHoldsIdleConnections(t *testing.T) {
	const idle = 8

	rt := playhold.NewRuntime(playhold.Config{Logger: slog.New(slog.DiscardHandler)})
	typ, err := cards.NewRoomType()
	require.NoError(t, err)
	server, err := playhold.NewServer(rt, &playhold.ServerConfig{RoomType: typ, MemoryLimit: -1})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got := holdIdle(ctx, &config{
		url:    "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/",
		report: time.Hour,
		idle:   idle,
	}, &bytes.Buffer{})

	assert.Equal(t, int64(idle), got)
}

func TestBotCountsFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// Nothing is listening at this address, so every game fails to connect.
	var out bytes.Buffer
	got := run(ctx, &config{url: "ws://127.0.0.1:1/", games: 1, report: time.Hour}, &out)

	assert.Positive(t, got.failures)
	assert.Zero(t, got.games)
	assert.Contains(t, out.String(), "failure: ")
}

func TestReportGroupsFailureReasonsAndLimitsThem(t *testing.T) {
	rec := &recorder{}
	rec.failure(errors.New("same"))
	rec.failure(errors.New("same"))
	for i := range maxReasons + 2 {
		rec.failure(fmt.Errorf("other %d", i))
	}

	var out bytes.Buffer
	rec.report(&out, time.Second)

	assert.Contains(t, out.String(), "failures=9")
	assert.Equal(t, maxReasons+1, strings.Count(out.String(), "failure: "))
	assert.Contains(t, out.String(), "failure: and 3 other reasons")

	// Reasons are counted since the last report, and the total keeps going.
	out.Reset()
	rec.failure(errors.New("same"))
	rec.report(&out, time.Second)

	assert.Contains(t, out.String(), "failures=10")
	assert.Contains(t, out.String(), "failure: 1x same")
}

func TestChooseMove(t *testing.T) {
	tests := []struct {
		name string
		hand *pb.HandsView
		want pb.HandsMessage
	}{
		{
			name: "plays when it can, even if it could draw",
			hand: &pb.HandsView{Playable: []string{"3-of-clubs"}, CanDraw: true},
			want: pb.HandsMessage_HANDS_MESSAGE_PLAY,
		},
		{name: "draws otherwise", hand: &pb.HandsView{CanDraw: true}, want: pb.HandsMessage_HANDS_MESSAGE_DRAW},
		{name: "passes last", hand: &pb.HandsView{CanPass: true}, want: pb.HandsMessage_HANDS_MESSAGE_PASS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := chooseMove(tt.hand)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChooseMoveAlwaysNamesASuitForAnEight(t *testing.T) {
	_, msg := chooseMove(&pb.HandsView{Playable: []string{"8-of-clubs"}})

	play, ok := msg.(*pb.Play)
	require.True(t, ok)
	assert.Equal(t, "8-of-clubs", play.GetCard())
	assert.NotEmpty(t, play.GetChosenSuit())
}

func TestPercentile(t *testing.T) {
	ten := make([]time.Duration, 0, 10)
	for i := range 10 {
		ten = append(ten, time.Duration(i+1))
	}

	tests := []struct {
		name   string
		sorted []time.Duration
		p      int
		want   time.Duration
	}{
		{name: "nothing recorded", sorted: nil, p: 50, want: 0},
		{name: "one value", sorted: ten[:1], p: 99, want: 1},
		{name: "middle", sorted: ten, p: 50, want: 5},
		{name: "near the top", sorted: ten, p: 99, want: 9},
		{name: "the top", sorted: ten, p: 100, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, percentile(tt.sorted, tt.p))
		})
	}
}

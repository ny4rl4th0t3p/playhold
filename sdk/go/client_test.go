package sdk_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
	sdk "github.com/ny4rl4th0t3p/playhold/sdk/go"
)

// These tests talk to a real server, so they check that both sides agree on the wire.

const (
	moduleName = "counter"
	code       = "room-1"
	schema     = 2

	msgAdd   uint32 = 1
	msgClose uint32 = 2
)

var totalKey = playhold.NewKey[int]("sdk-test-total")

// counter adds to a total, and its view shows it.
type counter struct{}

func (counter) Name() string { return moduleName }

func (counter) Register(r *playhold.Registry) {
	r.ClientSchema(schema, schema)
	playhold.State(r, totalKey, func() *int { return new(int) })
	playhold.Handle(r, msgAdd, func(rc *playhold.RoomContext, msg *wrapperspb.Int32Value) error {
		if msg.GetValue() < 0 {
			return errors.New("can't add less than nothing")
		}
		*totalKey.Get(rc) += int(msg.GetValue())
		return nil
	})
	playhold.Handle(r, msgClose, func(rc *playhold.RoomContext, _ *wrapperspb.Int32Value) error {
		rc.CloseRoom()
		return nil
	})
	playhold.View(r, func(rc *playhold.RoomContext) *wrapperspb.StringValue {
		return wrapperspb.String(strconv.Itoa(*totalKey.Get(rc)))
	})
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func startServer(t *testing.T) string {
	t.Helper()
	typ, err := playhold.NewRoomType(playhold.RoomConfig{Modules: []playhold.Module{counter{}}, MaxPlayers: 1})
	require.NoError(t, err)
	server, err := playhold.NewServer(playhold.NewRuntime(playhold.Config{Logger: slog.New(slog.DiscardHandler)}), &playhold.ServerConfig{
		RoomType:         typ,
		MemoryLimit:      -1,
		Logger:           slog.New(slog.DiscardHandler),
		Development:      true,
		AllowClientCodes: true,
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func options(url string, identity sdk.Identity) *sdk.Options {
	return &sdk.Options{URL: url, Identity: identity, SchemaVersions: map[string]uint32{moduleName: schema}}
}

func connect(t *testing.T, url string, identity sdk.Identity) *sdk.Client {
	t.Helper()
	c, err := sdk.Connect(testContext(t), options(url, identity))
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func viewOf(t *testing.T, views []sdk.ModuleView) string {
	t.Helper()
	require.Len(t, views, 1)
	assert.Equal(t, moduleName, views[0].Module)
	text := &wrapperspb.StringValue{}
	require.NoError(t, proto.Unmarshal(views[0].View, text))
	return text.GetValue()
}

// requireCode checks the error's code, and returns the number of the frame it's about.
func requireCode(t *testing.T, err error, want sdk.ErrorCode) (seq uint64) {
	t.Helper()
	var got *sdk.Error
	require.ErrorAs(t, err, &got)
	assert.Equal(t, want, got.Code)
	return got.Seq
}

func TestNewGuestGetsAnIdentityAndCanComeBack(t *testing.T) {
	url := startServer(t)

	first := connect(t, url, sdk.Identity{}).Identity()
	assert.NotEmpty(t, first.PlayerID)
	assert.NotEmpty(t, first.Secret)

	assert.Equal(t, first, connect(t, url, first).Identity(), "the secret isn't sent again, but it's kept")
}

func TestCreateRoomReturnsACodeOthersCanJoin(t *testing.T) {
	url := startServer(t)
	alice := connect(t, url, sdk.Identity{})

	made, err := alice.CreateRoom(testContext(t))

	require.NoError(t, err)
	assert.Len(t, made, 26)
	bob := connect(t, url, sdk.Identity{})
	requireCode(t, bob.Join(testContext(t), made), sdk.ErrorRoomFull) // the room has one seat, and alice has it
}

func TestServerRefusesAClient(t *testing.T) {
	url := startServer(t)

	tests := []struct {
		name string
		opts *sdk.Options
		want sdk.ErrorCode
	}{
		{
			name: "wrong secret",
			opts: options(url, sdk.Identity{PlayerID: "nobody", Secret: "wrong"}),
			want: sdk.ErrorUnauthorized,
		},
		{
			name: "too old",
			opts: &sdk.Options{URL: url, SchemaVersions: map[string]uint32{moduleName: schema - 1}},
			want: sdk.ErrorUpdateRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sdk.Connect(testContext(t), tt.opts)

			requireCode(t, err, tt.want)
		})
	}
}

func TestConnectReportsAServerThatIsNotThere(t *testing.T) {
	_, err := sdk.Connect(testContext(t), options("ws://127.0.0.1:1/", sdk.Identity{}))

	require.Error(t, err)
}

func TestJoinSendAndReceiveViews(t *testing.T) {
	c := connect(t, startServer(t), sdk.Identity{})
	ctx := testContext(t)

	require.NoError(t, c.Join(ctx, code))
	views, err := c.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0", viewOf(t, views))

	_, err = c.Send(ctx, moduleName, msgAdd, wrapperspb.Int32(3))
	require.NoError(t, err)
	views, err = c.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, "3", viewOf(t, views))
}

func TestFullRoomRefusesAJoin(t *testing.T) {
	url := startServer(t)
	require.NoError(t, connect(t, url, sdk.Identity{}).Join(testContext(t), code))

	requireCode(t, connect(t, url, sdk.Identity{}).Join(testContext(t), code), sdk.ErrorRoomFull)
}

func TestRefusedMessageIsReportedAndTheConnectionCarriesOn(t *testing.T) {
	c := connect(t, startServer(t), sdk.Identity{})
	ctx := testContext(t)
	require.NoError(t, c.Join(ctx, code))
	_, err := c.Next(ctx)
	require.NoError(t, err)

	seq, err := c.Send(ctx, moduleName, msgAdd, wrapperspb.Int32(-1))
	require.NoError(t, err)

	_, err = c.Next(ctx)
	assert.Equal(t, seq, requireCode(t, err, sdk.ErrorRejected))

	views, err := c.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0", viewOf(t, views))
}

func TestNextReportsAClosedRoom(t *testing.T) {
	c := connect(t, startServer(t), sdk.Identity{})
	ctx := testContext(t)
	require.NoError(t, c.Join(ctx, code))
	_, err := c.Next(ctx)
	require.NoError(t, err)

	_, err = c.Send(ctx, moduleName, msgClose, wrapperspb.Int32(0))
	require.NoError(t, err)

	for err == nil {
		_, err = c.Next(ctx)
	}
	require.ErrorIs(t, err, sdk.ErrRoomClosed)
}

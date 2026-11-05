package playhold

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// schemaModule is a module that talks to clients from version 2 up to version 3.
var schemaModule = registerFunc{name: "versioned", register: func(r *Registry) { r.ClientSchema(3, 2) }}

func helloWithVersions(versions map[string]uint32) *wire.ClientFrame {
	return &wire.ClientFrame{Seq: 1, Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: protocolVersion, SchemaVersions: versions},
	}}
}

func TestClientSchemaWindow(t *testing.T) {
	url := startGameServer(t, RoomConfig{Modules: []Module{schemaModule}}, 0)

	tests := []struct {
		name     string
		versions map[string]uint32
		accepted bool
	}{
		{name: "the current version", versions: map[string]uint32{"versioned": 3}, accepted: true},
		{name: "the oldest version", versions: map[string]uint32{"versioned": 2}, accepted: true},
		{name: "a newer client, while a deploy rolls out", versions: map[string]uint32{"versioned": 4}, accepted: true},
		{name: "too old", versions: map[string]uint32{"versioned": 1}},
		{name: "doesn't mention the module", versions: map[string]uint32{"other": 3}},
		{name: "mentions nothing", versions: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := dialServer(t, url)

			sendFrame(t, ws, helloWithVersions(tt.versions))

			frame := readServerFrame(t, ws)
			if tt.accepted {
				assert.NotNil(t, frame.GetWelcome())
				return
			}
			assert.Equal(t, wire.ErrorCode_ERROR_CODE_UPDATE_REQUIRED, frame.GetError().GetCode())
			assert.Equal(t, uint64(1), frame.GetError().GetSeq())
			assert.Contains(t, frame.GetError().GetMessage(), "versioned")
		})
	}
}

func TestOutdatedClientGetsNoIdentity(t *testing.T) {
	url := startGameServer(t, RoomConfig{Modules: []Module{schemaModule}}, 0)
	ws := dialServer(t, url)

	sendFrame(t, ws, helloWithVersions(nil))
	require.NotNil(t, readServerFrame(t, ws).GetError())

	// Saying hello failed, so it can't join anything.
	sendFrame(t, ws, joinFrame(testCode))
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, readServerFrame(t, ws).GetError().GetCode())
}

func TestModuleWithoutAClientSchemaAcceptsEveryClient(t *testing.T) {
	url := startGameServer(t, RoomConfig{Modules: []Module{savedModule}}, 0)
	ws := dialServer(t, url)

	sendFrame(t, ws, helloWithVersions(nil))

	assert.NotNil(t, readServerFrame(t, ws).GetWelcome())
}

func TestEveryModuleWithASchemaIsChecked(t *testing.T) {
	other := registerFunc{name: "other", register: func(r *Registry) { r.ClientSchema(1, 1) }}
	url := startGameServer(t, RoomConfig{Modules: []Module{schemaModule, other}}, 0)
	ws := dialServer(t, url)

	sendFrame(t, ws, helloWithVersions(map[string]uint32{"versioned": 3}))

	got := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_UPDATE_REQUIRED, got.GetCode())
	assert.Contains(t, got.GetMessage(), "other")
}

func TestClientSchemaWiringMistakes(t *testing.T) {
	tests := []struct {
		name     string
		register func(r *Registry)
		want     string
	}{
		{name: "oldest above current", register: func(r *Registry) { r.ClientSchema(1, 2) }, want: "oldest <= current"},
		{name: "oldest below one", register: func(r *Registry) { r.ClientSchema(1, 0) }, want: "1 <= oldest"},
		{
			name: "declared twice",
			register: func(r *Registry) {
				r.ClientSchema(2, 1)
				r.ClientSchema(2, 2)
			},
			want: "declared twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRoomType(RoomConfig{Modules: []Module{registerFunc{name: "m", register: tt.register}}})

			require.ErrorContains(t, err, tt.want)
		})
	}
}

package playhold

import (
	"errors"
	"testing"
	"testing/synctest"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	msgAdd uint32 = iota + 1
	msgFail
)

// addMsg is a ready-made Protobuf message, so these tests need no generated code.
type addMsg = wrapperspb.Int32Value

// counterModule is a module with state and handlers.
var counterKey = NewKey[counterState]("counter")

type counterState struct{ value int }

type counterModule struct{}

func (counterModule) Name() string { return "counter" }

func (counterModule) Register(r *Registry) {
	State(r, counterKey, func() *counterState { return &counterState{} })
	Handle(r, msgAdd, func(rc *RoomContext, msg *addMsg) error {
		counterKey.Get(rc).value += int(msg.GetValue())
		return nil
	})
}

// registerFunc is a module defined by a function, for tests that need one specific registration.
type registerFunc struct {
	name     string
	register func(r *Registry)
}

func (m registerFunc) Name() string { return m.name }

func (m registerFunc) Register(r *Registry) { m.register(r) }

// runInRoom runs fn on the room's goroutine and waits for it to finish.
func runInRoom(t *testing.T, room *Room, fn func(rc *RoomContext)) {
	t.Helper()
	finished := make(chan struct{})
	require.NoError(t, room.submit(input{run: func(rc *RoomContext) error {
		defer close(finished)
		fn(rc)
		return nil
	}}))
	waitFor(t, finished)
}

// dispatchAndWait sends a message and returns the handler's result.
func dispatchAndWait(t *testing.T, room *Room, module string, id uint32, msg proto.Message) error {
	t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	results := make(chan error, 1)
	require.NoError(t, room.dispatch("alice", module, id, payload, func(err error) { results <- err }))
	return <-results
}

func TestModuleStateIsSeparateInEachRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)
		typ := newTestRoomType(t, RoomConfig{Modules: []Module{counterModule{}}})
		first, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		second, err := rt.CreateRoom(typ)
		require.NoError(t, err)

		require.NoError(t, dispatchAndWait(t, first, "counter", msgAdd, &addMsg{Value: 3}))
		require.NoError(t, dispatchAndWait(t, second, "counter", msgAdd, &addMsg{Value: 5}))

		runInRoom(t, first, func(rc *RoomContext) { assert.Equal(t, 3, counterKey.Get(rc).value) })
		runInRoom(t, second, func(rc *RoomContext) { assert.Equal(t, 5, counterKey.Get(rc).value) })
	})
}

func TestModuleStateIsReadyForInitHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		value := -1
		newTestRoom(t, newTestRuntime(t), RoomConfig{
			Modules: []Module{counterModule{}},
			Init: func(rc *RoomContext) error {
				value = counterKey.Get(rc).value
				return nil
			},
		})

		assert.Equal(t, 0, value)
	})
}

func TestStateOfMissingModuleClosesOnlyThatRoom(t *testing.T) {
	// Created after counterKey, so its room has a slot at counterKey's position, left empty.
	laterKey := NewKey[counterState]("later")
	withLaterKey := registerFunc{name: "later", register: func(r *Registry) {
		State(r, laterKey, func() *counterState { return &counterState{} })
	}}

	tests := []struct {
		name    string
		modules []Module
	}{
		{name: "room with no slots", modules: nil},
		{name: "room with an empty slot", modules: []Module{withLaterKey}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := newTestRuntime(t)
				room := newTestRoom(t, rt, RoomConfig{Modules: tt.modules})
				other := newTestRoom(t, rt, RoomConfig{})

				require.NoError(t, room.submit(input{run: func(rc *RoomContext) error {
					counterKey.Get(rc)
					return nil
				}}))
				waitFor(t, room.Done())

				assert.Equal(t, ClosePanic, room.Wait())
				runInRoom(t, other, func(_ *RoomContext) {})
			})
		})
	}
}

func TestFailedInitDropsItsEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		delivered := false
		module := registerFunc{name: "m", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ firstEvent) { delivered = true })
		}}
		errInit := errors.New("bad config")

		_, err := newTestRuntime(t).CreateRoom(newTestRoomType(t, RoomConfig{
			Modules: []Module{module},
			Init: func(rc *RoomContext) error {
				rc.Emit(firstEvent{})
				return errInit
			},
		}))

		require.ErrorIs(t, err, errInit)
		assert.False(t, delivered)
	})
}

func TestDispatchReportsMessageProblems(t *testing.T) {
	errRule := errors.New("not your turn")
	failing := registerFunc{name: "failing", register: func(r *Registry) {
		Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error { return errRule })
	}}

	tests := []struct {
		name   string
		module string
		id     uint32
		want   error
	}{
		{name: "unknown module", module: "nope", id: msgAdd, want: ErrUnknownMessage},
		{name: "unknown message", module: "counter", id: msgFail, want: ErrUnknownMessage},
		{name: "handler error", module: "failing", id: msgFail, want: errRule},
		{name: "success", module: "counter", id: msgAdd, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{counterModule{}, failing}})

				require.ErrorIs(t, dispatchAndWait(t, room, tt.module, tt.id, &addMsg{Value: 1}), tt.want)

				// None of these closes the room.
				runInRoom(t, room, func(_ *RoomContext) {})
			})
		})
	}
}

func TestUndecodablePayloadIsABadMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{counterModule{}}})

		results := make(chan error, 1)
		require.NoError(t, room.dispatch("alice", "counter", msgAdd, []byte{0xff}, func(err error) { results <- err }))

		require.ErrorIs(t, <-results, ErrBadMessage)
		runInRoom(t, room, func(_ *RoomContext) {})
	})
}

func TestSameMessageIDInDifferentModules(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		handlerFor := func(name string) registerFunc {
			return registerFunc{name: name, register: func(r *Registry) {
				Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error {
					got = append(got, name)
					return nil
				})
			}}
		}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{handlerFor("a"), handlerFor("b")}})

		require.NoError(t, dispatchAndWait(t, room, "b", msgAdd, &addMsg{}))
		require.NoError(t, dispatchAndWait(t, room, "a", msgAdd, &addMsg{}))

		assert.Equal(t, []string{"b", "a"}, got)
	})
}

func TestNewRoomTypeReportsWiringMistakes(t *testing.T) {
	dupKey := NewKey[counterState]("dup")
	newState := func() *counterState { return &counterState{} }
	noop := func(_ *RoomContext, _ *addMsg) error { return nil }

	tests := []struct {
		name    string
		modules []Module
		want    string
	}{
		{name: "nil module", modules: []Module{nil}, want: "is nil"},
		{name: "unnamed module", modules: []Module{registerFunc{register: func(_ *Registry) {}}}, want: "has no name"},
		{name: "duplicate module", modules: []Module{counterModule{}, counterModule{}}, want: "appears twice"},
		{
			name: "duplicate state",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				State(r, dupKey, newState)
				State(r, dupKey, newState)
			}}},
			want: "registered twice",
		},
		{
			name: "state without initializer",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				State(r, dupKey, nil)
			}}},
			want: "no initializer",
		},
		{
			name: "duplicate message",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				Handle(r, msgAdd, noop)
				Handle(r, msgAdd, noop)
			}}},
			want: "registered twice",
		},
		{
			name: "message without handler",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				Handle[addMsg](r, msgAdd, nil)
			}}},
			want: "no handler",
		},
		{
			name: "interface event",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				On(r, func(_ *RoomContext, _ error) {})
			}}},
			want: "is an interface",
		},
		{
			name: "event without handler",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				On[addMsg](r, nil)
			}}},
			want: "no handler",
		},
		{
			name: "keeper not wired",
			modules: []Module{counterModule{}, registerFunc{name: "m", register: func(r *Registry) {
				r.Needs("counter", nil)
			}}},
			want: "none was wired in",
		},
		{
			name: "provider not in room type",
			modules: []Module{registerFunc{name: "m", register: func(r *Registry) {
				r.Needs("counter", struct{}{})
			}}},
			want: "not in the room type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ, err := NewRoomType(RoomConfig{Name: "match", Modules: tt.modules})

			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, typ)
		})
	}
}

func TestNewRoomTypeReportsEveryMistake(t *testing.T) {
	_, err := NewRoomType(RoomConfig{Modules: []Module{
		nil,
		registerFunc{name: "m", register: func(r *Registry) {
			r.Needs("first", struct{}{})
			r.Needs("second", struct{}{})
		}},
	}})

	require.ErrorContains(t, err, "is nil")
	require.ErrorContains(t, err, `"first"`)
	require.ErrorContains(t, err, `"second"`)
}

// pingKeeper and pongKeeper are declared by the modules that need them, so ping and pong can call each other
// in both directions, as modules in separate packages would.
type pingKeeper interface {
	AddPing(rc *RoomContext)
}

type pongKeeper interface {
	ReturnPing(rc *RoomContext)
}

var pingStateKey = NewKey[counterState]("ping")

type pingModule struct {
	pong pongKeeper
}

func (*pingModule) Name() string { return "ping" }

func (m *pingModule) Register(r *Registry) {
	r.Needs("pong", m.pong)
	State(r, pingStateKey, func() *counterState { return &counterState{} })
	Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
		m.pong.ReturnPing(rc)
		return nil
	})
}

func (*pingModule) AddPing(rc *RoomContext) { pingStateKey.Get(rc).value++ }

type pongModule struct {
	ping pingKeeper
}

func (*pongModule) Name() string { return "pong" }

func (m *pongModule) Register(r *Registry) { r.Needs("ping", m.ping) }

func (m *pongModule) ReturnPing(rc *RoomContext) { m.ping.AddPing(rc) }

func TestModulesCanCallEachOtherThroughKeepers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ping := &pingModule{}
		pong := &pongModule{ping: ping}
		ping.pong = pong
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{ping, pong}})

		require.NoError(t, dispatchAndWait(t, room, "ping", msgAdd, &addMsg{}))

		runInRoom(t, room, func(rc *RoomContext) { assert.Equal(t, 1, pingStateKey.Get(rc).value) })
	})
}

type firstEvent struct{}

type secondEvent struct{}

type loopEvent struct{}

func TestEventsArriveInOrderAfterTheHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		emitter := registerFunc{name: "emitter", register: func(r *Registry) {
			Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
				rc.Emit(firstEvent{})
				got = append(got, "handler done")
				return nil
			})
			On(r, func(rc *RoomContext, _ firstEvent) {
				got = append(got, "emitter got first")
				rc.Emit(secondEvent{})
			})
		}}
		listener := registerFunc{name: "listener", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ firstEvent) { got = append(got, "listener got first") })
			On(r, func(_ *RoomContext, _ secondEvent) { got = append(got, "listener got second") })
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{emitter, listener}})

		results := make(chan error, 1)
		require.NoError(t, room.dispatch("alice", "emitter", msgAdd, nil, func(err error) {
			got = append(got, "result")
			results <- err
		}))
		require.NoError(t, <-results)

		assert.Equal(t, []string{
			"handler done",
			"emitter got first",
			"listener got first",
			"listener got second",
			"result",
		}, got)
	})
}

func TestEventWithNoSubscribersIsIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

		runInRoom(t, room, func(rc *RoomContext) { rc.Emit(firstEvent{}) })
		runInRoom(t, room, func(_ *RoomContext) {})
	})
}

func TestHandlersAndEventsSeeTheSender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var inHandler, inEvent PlayerID
		module := registerFunc{name: "m", register: func(r *Registry) {
			Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
				inHandler = rc.Player()
				rc.Emit(firstEvent{})
				return nil
			})
			On(r, func(rc *RoomContext, _ firstEvent) { inEvent = rc.Player() })
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{module}})

		require.NoError(t, dispatchAndWait(t, room, "m", msgAdd, &addMsg{}))

		assert.Equal(t, PlayerID("alice"), inHandler)
		assert.Equal(t, PlayerID("alice"), inEvent)
	})
}

func TestEventLoopHitsDepthLimitAndClosesRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deliveries := 0
		looping := registerFunc{name: "looping", register: func(r *Registry) {
			On(r, func(rc *RoomContext, _ loopEvent) {
				deliveries++
				rc.Emit(loopEvent{})
			})
		}}
		rt := newTestRuntime(t)
		room := newTestRoom(t, rt, RoomConfig{Modules: []Module{looping}})
		other := newTestRoom(t, rt, RoomConfig{})

		require.NoError(t, room.submit(input{run: func(rc *RoomContext) error {
			rc.Emit(loopEvent{})
			return nil
		}}))
		waitFor(t, room.Done())

		assert.Equal(t, ClosePanic, room.Wait())
		assert.Equal(t, maxEventDepth, deliveries)
		runInRoom(t, other, func(_ *RoomContext) {})
	})
}

func TestEventsEmittedByLifecycleHooksAreDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		module := registerFunc{name: "m", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ firstEvent) { got = append(got, "init event") })
			On(r, func(_ *RoomContext, _ secondEvent) { got = append(got, "close event") })
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{
			Modules: []Module{module},
			Init: func(rc *RoomContext) error {
				rc.Emit(firstEvent{})
				return nil
			},
			Close: func(rc *RoomContext, _ CloseReason) { rc.Emit(secondEvent{}) },
		})

		room.Close()
		waitFor(t, room.Done())

		assert.Equal(t, []string{"init event", "close event"}, got)
	})
}

func TestABuiltRoomTypeKeepsNoneOfTheCallersModuleList(t *testing.T) {
	modules := []Module{counterModule{}}
	typ := newTestRoomType(t, RoomConfig{Modules: modules})

	modules[0] = nil // the caller's slice, which a copy of the config would share

	assert.Nil(t, typ.cfg.Modules)
	_, registered := typ.handlers[handlerKey{module: counterModule{}.Name(), id: msgAdd}]
	assert.True(t, registered, "what the module registered stays")
}

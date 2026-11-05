package playhold

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// TimerID identifies a pending timer, so it can be canceled. A module can keep it in its state.
type TimerID uint64

// TimersAfterRestart says what happens to pending timers when a server comes back. A game decides this, because it's a
// question of what's fair to its players. It doesn't apply to a room restored after a crash while the server stays up:
// no time was lost, and a timer that crashes the room has to keep firing until the room's restart limit stops it.
type TimersAfterRestart int

const (
	// TimersPause is the default: the time the server was down doesn't count, so nobody loses a turn to an outage. It's
	// measured from the room's last turn, or from the shutdown if there was one, so after a crash a player can get back
	// a little more time than they had, but never less.
	TimersPause TimersAfterRestart = iota
	// TimersKeepRunning keeps each timer's original time. Any that came due while the server was down fire at once, in
	// order.
	TimersKeepRunning
	// TimersCancel drops every pending timer. A module that relies on one has to set it again.
	TimersCancel
)

// applyRestartPolicy adjusts timers just restored from a snapshot saved at the room's current clock.
func (r *Room) applyRestartPolicy(policy TimersAfterRestart) {
	switch policy {
	case TimersKeepRunning:
	case TimersCancel:
		r.timers = nil
	case TimersPause:
		down := r.clock().Sub(r.now)
		if down <= 0 {
			return
		}
		for i := range r.timers {
			r.timers[i].due = r.timers[i].due.Add(down)
		}
	}
}

// TimerLeft returns how long until a pending timer fires, measured from the start of this turn, which keeps it
// reproducible. It reports false if the timer isn't pending. A view can use it to show a countdown.
func (rc *RoomContext) TimerLeft(id TimerID) (time.Duration, bool) {
	r := rc.room
	at := slices.IndexFunc(r.timers, func(t roomTimer) bool { return t.id == id })
	if at < 0 {
		return 0, false
	}
	return r.timers[at].due.Sub(r.now), true
}

// roomTimer is an event waiting for its time.
type roomTimer struct {
	id    TimerID
	due   time.Time
	event proto.Message
}

func compareTimers(a, b roomTimer) int {
	if order := a.due.Compare(b.due); order != 0 {
		return order
	}
	return cmp.Compare(a.id, b.id)
}

// After emits the event after this much time, in a turn of its own, to the modules subscribed to it with On. Timers
// are saved with the room, so they survive a restart, as ServerConfig.TimersAfterRestart says. The event has to be a
// Protobuf message, because it's saved too, and it's handled as that pointer type:
//
//	rc.After(time.Minute, &pb.TurnExpired{})
//	playhold.On(r, func(rc *playhold.RoomContext, event *pb.TurnExpired) { ... })
//
// Player is empty in a turn started by a timer. A view can't set timers.
func (rc *RoomContext) After(d time.Duration, event proto.Message) TimerID {
	if rc.viewing {
		panic(fmt.Sprintf("playhold: a view can't set timers; got %T", event))
	}
	if event == nil {
		panic("playhold: a timer needs an event")
	}
	r := rc.room
	r.lastTimerID++
	r.insertTimer(roomTimer{id: r.lastTimerID, due: r.now.Add(d), event: event})
	return r.lastTimerID
}

// CancelTimer stops a pending timer, and reports whether it was still pending.
func (rc *RoomContext) CancelTimer(id TimerID) bool {
	r := rc.room
	at := slices.IndexFunc(r.timers, func(t roomTimer) bool { return t.id == id })
	if at < 0 {
		return false
	}
	r.timers = slices.Delete(r.timers, at, at+1)
	return true
}

func (r *Room) insertTimer(timer roomTimer) {
	at, _ := slices.BinarySearchFunc(r.timers, timer, compareTimers)
	r.timers = slices.Insert(r.timers, at, timer)
}

// armAlarm makes sure the alarm goes off for the soonest timer. It runs before the room waits for more work.
func (r *Room) armAlarm() {
	if len(r.timers) == 0 {
		if r.alarmSet {
			r.alarm.Stop()
			r.alarmSet = false
		}
		return
	}
	due := r.timers[0].due
	if r.alarmSet && due.Equal(r.alarmDue) {
		return
	}
	r.alarm.Reset(due.Sub(r.clock()))
	r.alarmSet, r.alarmDue = true, due
}

// fireNext runs a turn for the soonest timer, if it's due, and reports whether that turn panicked. The timer is removed
// first, but the last snapshot still has it, so a timer that crashes the room fires again after it's restored, until
// the room's restart limit closes it.
func (r *Room) fireNext() (panicked bool) {
	r.alarmSet = false
	if len(r.timers) == 0 || r.timers[0].due.After(r.clock()) {
		return false
	}
	timer := r.timers[0]
	r.timers = slices.Delete(r.timers, 0, 1)
	return r.turn(input{timerEvent: string(proto.MessageName(timer.event)), run: func(rc *RoomContext) error {
		rc.Emit(timer.event)
		return nil
	}})
}

func (r *Room) encodeTimers() []*wire.PendingTimer {
	timers := make([]*wire.PendingTimer, 0, len(r.timers))
	for i := range r.timers {
		timer := &r.timers[i]
		event, err := proto.Marshal(timer.event)
		if err != nil {
			panic(fmt.Sprintf("playhold: timer event %T can't be saved: %v", timer.event, err))
		}
		timers = append(timers, &wire.PendingTimer{
			Id:           uint64(timer.id),
			DueUnixNanos: timer.due.UnixNano(),
			EventType:    string(proto.MessageName(timer.event)),
			Event:        event,
		})
	}
	return timers
}

func (r *Room) decodeTimers(saved []*wire.PendingTimer) error {
	r.timers = make([]roomTimer, 0, len(saved))
	for _, timer := range saved {
		messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(timer.GetEventType()))
		if err != nil {
			return fmt.Errorf("playhold: timer event %q: %w", timer.GetEventType(), err)
		}
		event := messageType.New().Interface()
		if err := proto.Unmarshal(timer.GetEvent(), event); err != nil {
			return fmt.Errorf("playhold: timer event %q: %w", timer.GetEventType(), err)
		}
		r.insertTimer(roomTimer{id: TimerID(timer.GetId()), due: time.Unix(0, timer.GetDueUnixNanos()), event: event})
	}
	return nil
}

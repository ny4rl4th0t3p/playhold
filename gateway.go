package playhold

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const (
	// protocolVersion is the version of the frames in proto/playhold/v1.
	protocolVersion = 1
	maxRoomCodeLen  = 32
	storeTimeout    = 5 * time.Second
)

// gateway connects client connections to guests and rooms.
type gateway struct {
	guests      *guests
	lobby       *lobby
	logger      *slog.Logger
	newGuests   *perIP // nil if off
	messageRate RateLimit
}

func (g *gateway) connect(c *transport.Conn) (func(*wire.ClientFrame), error) {
	s := &session{gateway: g, conn: c, frames: g.messageRate.limiter()}
	c.OnClose(s.ended)
	return s.frame, nil
}

// session is one connection's progress: Hello, then Join, then module messages.
type session struct {
	gateway *gateway
	conn    *transport.Conn

	// Only used on the connection's read goroutine.
	player   PlayerID
	frames   *rate.Limiter                // every frame; nil if off
	messages map[handlerKey]*rate.Limiter // messages a module limits, created when first sent

	// The room's goroutine reports the result of a Join, so these need the lock.
	mu     sync.Mutex
	room   *Room
	code   string
	joined bool
	failed chan struct{} // closed if the Join fails, to stop watching the room
	result func(error)   // answers the Join, whichever copy of the room seats the player
}

func (s *session) frame(frame *wire.ClientFrame) {
	seq := frame.GetSeq()
	if !allow(s.frames) {
		s.rateLimited(seq)
		return
	}
	switch body := frame.GetBody().(type) {
	case *wire.ClientFrame_Hello:
		s.hello(seq, body.Hello)
	case *wire.ClientFrame_Join:
		s.join(seq, body.Join.GetCode())
	case *wire.ClientFrame_Message:
		s.message(seq, body.Message)
	default:
		s.malformed(seq, "frame has no body")
	}
}

func (s *session) malformed(seq uint64, message string) {
	s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, message)
}

// rateLimited answers a frame refused for coming too fast, so the client knows it wasn't handled.
func (s *session) rateLimited(seq uint64) {
	s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_RATE_LIMITED, "too many requests; slow down")
}

func (s *session) hello(seq uint64, hello *wire.Hello) {
	if s.player != "" {
		s.malformed(seq, "already said hello")
		return
	}
	if hello.GetProtocolVersion() != protocolVersion {
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UNSUPPORTED_PROTOCOL, "unsupported protocol version")
		return
	}
	if module := s.gateway.lobby.typ.outdatedModule(hello.GetSchemaVersions()); module != "" {
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UPDATE_REQUIRED, "this client is too old for module "+module)
		return
	}

	// This waits on the database, but only holds up this client, whose read goroutine it's running on.
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	guests := s.gateway.guests
	player, secret := PlayerID(hello.GetPlayerId()), ""
	var err error
	if player == "" && hello.GetSecret() == "" {
		if !s.gateway.newGuests.allow(s.conn.IP()) {
			s.rateLimited(seq)
			return
		}
		player, secret, err = guests.create(ctx)
	} else {
		var known bool
		known, err = guests.verify(ctx, player, hello.GetSecret())
		if err == nil && !known {
			s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UNAUTHORIZED, "unknown player or wrong secret")
			return
		}
	}
	if err != nil {
		s.gateway.logger.Error("guest store failed", "error", err)
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, "the server can't check players right now")
		return
	}

	s.player = player
	if old := guests.attach(player, s.conn); old != nil {
		old.Replace()
	}
	s.conn.HandshakeDone()
	s.conn.SendWelcome(string(player), secret)
}

func (s *session) join(seq uint64, code string) {
	switch {
	case s.player == "":
		s.malformed(seq, "say hello before joining")
		return
	case !validRoomCode(code):
		s.malformed(seq, "invalid room code")
		return
	}

	s.mu.Lock()
	busy := s.room != nil && !s.room.closed()
	s.mu.Unlock()
	if busy {
		s.malformed(seq, "already in a room")
		return
	}

	room, err := s.gateway.lobby.enter(code)
	switch {
	case errors.Is(err, ErrTooManyRooms):
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_SERVER_FULL, "the server has no space for more games")
		return
	case errors.Is(err, ErrDraining):
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, err.Error())
		return
	case err != nil:
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_REJECTED, "the room could not be created")
		return
	}

	failed := make(chan struct{})
	result := func(err error) { s.seated(seq, code, err) }
	s.mu.Lock()
	s.room, s.code, s.joined, s.failed, s.result = room, code, false, failed, result
	s.mu.Unlock()
	go s.watchRoom(room, failed)

	if err := room.seat(s.player, s.conn, result); err != nil {
		result(err)
	}
}

// seated finishes a Join. It runs on the room's goroutine, unless the request never reached the room.
func (s *session) seated(seq uint64, code string, err error) {
	s.mu.Lock()
	room, failed := s.room, s.failed
	if err == nil {
		s.joined = true
	} else {
		s.room = nil
	}
	s.mu.Unlock()

	if err == nil {
		s.conn.SendJoined(seq, code)
		return
	}
	if room != nil { // otherwise the connection has already ended
		close(failed)
		s.gateway.lobby.leave(code, room)
	}
	if errors.Is(err, ErrRoomClosed) {
		// The room stopped just as the player arrived, and it may be coming back after a crash or a restart. A client
		// forgets a room that refuses it, so this asks it to try again, and a room that's really gone becomes a new one.
		s.conn.SendError(seq, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, "the room is restarting")
		return
	}
	s.conn.SendError(seq, wireErrorCode(err), err.Error())
}

// watchRoom tells the client when its room closes.
func (s *session) watchRoom(room *Room, failed <-chan struct{}) {
	for {
		select {
		case <-room.Done():
			s.mu.Lock()
			code, current := s.code, s.room == room
			s.mu.Unlock()
			if !current {
				return // the connection has ended, or moved on from this room
			}
			if room.Wait() == CloseShutdown {
				return // the room will be back, so the client should reconnect to it, not forget it
			}
			next := s.gateway.lobby.replacement(code, room)
			if next == nil {
				s.conn.SendClosed(wireCloseReason(room.Wait()))
				return
			}
			s.follow(room, next)
			room = next
		case <-failed:
			return
		case <-s.conn.Done():
			return
		}
	}
}

// follow moves this connection to the copy of its room restored after a crash, which sends the player a full view. A
// Join still in the old room's queue was dropped with it, so this seat answers that Join.
func (s *session) follow(old, next *Room) {
	s.mu.Lock()
	if s.room != old {
		s.mu.Unlock()
		return
	}
	s.room = next
	var result func(error)
	if !s.joined {
		result = s.result
	}
	s.mu.Unlock()

	err := next.seat(s.player, s.conn, result)
	switch {
	case err == nil:
	case result != nil:
		result(err)
	default:
		s.gateway.logger.Warn("rejoining a restored room failed", "player", string(s.player), "error", err)
	}
}

func (s *session) message(seq uint64, msg *wire.ModuleMessage) {
	s.mu.Lock()
	room, joined := s.room, s.joined
	s.mu.Unlock()

	switch {
	case !joined:
		s.malformed(seq, "join a room before sending messages")
		return
	case msg.GetModule() == "":
		s.malformed(seq, "message has no module")
		return
	case !s.allowMessage(handlerKey{module: msg.GetModule(), id: msg.GetId()}):
		s.rateLimited(seq)
		return
	}
	report := func(err error) {
		if err != nil {
			s.conn.SendError(seq, wireErrorCode(err), err.Error())
		}
	}
	report(room.dispatch(s.player, msg.GetModule(), msg.GetId(), msg.GetPayload(), report))
}

// allowMessage applies the limit a module set on this message, if any.
func (s *session) allowMessage(key handlerKey) bool {
	limit, ok := s.gateway.lobby.typ.limits[key]
	if !ok {
		return true
	}
	limiter := s.messages[key]
	if limiter == nil {
		if s.messages == nil {
			s.messages = make(map[handlerKey]*rate.Limiter)
		}
		limiter = limit.limiter()
		s.messages[key] = limiter
	}
	return limiter.Allow()
}

// ended releases what the connection held. The player keeps their seat.
func (s *session) ended() {
	if s.player != "" {
		s.gateway.guests.detach(s.player, s.conn)
	}

	s.mu.Lock()
	room, code := s.room, s.code
	s.room = nil
	s.mu.Unlock()

	if room != nil {
		room.removeViewer(s.player, s.conn)
		s.gateway.lobby.leave(code, room)
	}
}

// validRoomCode accepts codes that are safe to put in a link.
func validRoomCode(code string) bool {
	if code == "" || len(code) > maxRoomCodeLen {
		return false
	}
	for _, c := range code {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func wireErrorCode(err error) wire.ErrorCode {
	switch {
	case errors.Is(err, ErrUnknownMessage):
		return wire.ErrorCode_ERROR_CODE_UNKNOWN_MESSAGE
	case errors.Is(err, ErrBadMessage):
		return wire.ErrorCode_ERROR_CODE_BAD_MESSAGE
	case errors.Is(err, ErrQueueFull):
		return wire.ErrorCode_ERROR_CODE_QUEUE_FULL
	case errors.Is(err, ErrRoomClosed):
		return wire.ErrorCode_ERROR_CODE_ROOM_CLOSED
	case errors.Is(err, ErrRoomFull):
		return wire.ErrorCode_ERROR_CODE_ROOM_FULL
	case errors.Is(err, ErrDraining):
		return wire.ErrorCode_ERROR_CODE_UNAVAILABLE
	default:
		return wire.ErrorCode_ERROR_CODE_REJECTED
	}
}

func wireCloseReason(reason CloseReason) wire.CloseReason {
	switch reason {
	case CloseRequested:
		return wire.CloseReason_CLOSE_REASON_REQUESTED
	case CloseShutdown:
		return wire.CloseReason_CLOSE_REASON_SHUTDOWN
	case ClosePanic:
		return wire.CloseReason_CLOSE_REASON_PANIC
	case CloseStuck:
		return wire.CloseReason_CLOSE_REASON_STUCK
	case CloseInactive:
		return wire.CloseReason_CLOSE_REASON_INACTIVE
	default:
		return wire.CloseReason_CLOSE_REASON_UNSPECIFIED
	}
}

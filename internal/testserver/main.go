// Command testserver is a small game server for testing SDKs against a real server. It prints its URL, then serves
// until killed.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ny4rl4th0t3p/playhold"
)

const (
	msgAdd uint32 = iota + 1
	msgClose
)

const (
	maxPlayers        = 2
	clientSchema      = 2 // not 1, so a client below it can be tested
	readHeaderTimeout = 5 * time.Second
)

type total struct{ value int32 }

var totalKey = playhold.NewKey[total]("counter")

// counter adds to a total shared by the room, and shows each player who they are and the total.
type counter struct{}

func (counter) Name() string { return "counter" }

func (counter) Register(r *playhold.Registry) {
	r.ClientSchema(clientSchema, clientSchema)
	playhold.State(r, totalKey, func() *total { return &total{} })
	playhold.Handle(r, msgAdd, func(rc *playhold.RoomContext, msg *wrapperspb.Int32Value) error {
		if msg.GetValue() <= 0 {
			return errors.New("the amount must be positive")
		}
		totalKey.Get(rc).value += msg.GetValue()
		return nil
	})
	playhold.Handle(r, msgClose, func(rc *playhold.RoomContext, _ *wrapperspb.Int32Value) error {
		rc.CloseRoom()
		return nil
	})
	playhold.View(r, func(rc *playhold.RoomContext) *wrapperspb.StringValue {
		return wrapperspb.String(fmt.Sprintf("%s:%d", rc.Player(), totalKey.Get(rc).value))
	})
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	typ, err := playhold.NewRoomType(playhold.RoomConfig{
		Name:       "test",
		MaxPlayers: maxPlayers,
		Modules:    []playhold.Module{counter{}},
	})
	if err != nil {
		return err
	}
	// Development: it serves plain HTTP on localhost, for the SDKs' tests, which also pick their rooms' codes.
	server, err := playhold.NewServer(playhold.NewRuntime(playhold.Config{}), &playhold.ServerConfig{
		RoomType: typ, Development: true, AllowClientCodes: true,
	})
	if err != nil {
		return err
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	fmt.Printf("ws://%s\n", listener.Addr())

	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: readHeaderTimeout}
	return httpServer.Serve(listener)
}

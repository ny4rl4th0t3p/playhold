package playhold_test

import (
	"context"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ny4rl4th0t3p/playhold"
)

// These examples are the code the documentation site shows, between the region comments. They compile with the tests,
// but don't run, since a server serves until it's stopped.

// matchType stands in for a game's room type.
func matchType() *playhold.RoomType {
	typ, err := playhold.NewRoomType(playhold.RoomConfig{Name: "match"})
	if err != nil {
		log.Fatal(err)
	}
	return typ
}

// onTheInternet is the usual way to listen: on port 443, with automatic certificates.
func onTheInternet() *playhold.ListenConfig {
	return &playhold.ListenConfig{AutocertDomains: []string{"game.example"}, AutocertCacheDir: "/var/lib/game/certs"}
}

// #region run
// run serves a game until Ctrl+C or SIGTERM, then stops without losing a match, as a game's main function would.
func run(cfg *playhold.ServerConfig, listen *playhold.ListenConfig) {
	server, err := playhold.NewServer(playhold.NewRuntime(playhold.Config{}), cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = server.ListenAndServe(ctx, listen)
	stop()
	if err != nil {
		// With RestartWhenStuck this includes ErrRestartNeeded: exiting with an error makes the service manager start
		// the server again.
		log.Fatal(err)
	}
}

// #endregion run

// Automatic certificates from Let's Encrypt, on port 443.
func ExampleServer_ListenAndServe() {
	// #region autocert
	run(&playhold.ServerConfig{RoomType: matchType()}, &playhold.ListenConfig{
		AutocertDomains:  []string{"game.example"},
		AutocertCacheDir: "/var/lib/game/certs",
		AutocertEmail:    "admin@game.example", // optional: where Let's Encrypt writes about problems
	})
	// #endregion autocert
}

// A first run against Let's Encrypt's test service, which has no limits on failed attempts. Browsers don't trust its
// certificates.
func ExampleServer_ListenAndServe_staging() {
	// #region staging
	run(&playhold.ServerConfig{RoomType: matchType()}, &playhold.ListenConfig{
		AutocertDomains:      []string{"game.example"},
		AutocertCacheDir:     "/var/lib/game/certs-staging", // never the real cache folder
		AutocertDirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory",
	})
	// #endregion staging
}

// A certificate the server already has.
func ExampleServer_ListenAndServe_certificateFiles() {
	// #region certificate-files
	run(&playhold.ServerConfig{RoomType: matchType()}, &playhold.ListenConfig{
		CertFile: "/etc/game/fullchain.pem",
		KeyFile:  "/etc/game/privkey.pem",
	})
	// #endregion certificate-files
}

// Plain HTTP on port 8080, behind a proxy at 10.0.0.2 that ends TLS.
func ExampleServer_ListenAndServe_behindAProxy() {
	// #region behind-a-proxy
	run(&playhold.ServerConfig{
		RoomType:       matchType(),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
	}, &playhold.ListenConfig{})
	// #endregion behind-a-proxy
}

// A developer's machine: plain HTTP, and none of the checks that keep a public server safe.
func ExampleServerConfig_development() {
	// #region development
	run(&playhold.ServerConfig{RoomType: matchType(), Development: true}, &playhold.ListenConfig{Addr: "127.0.0.1:8080"})
	// #endregion development
}

func ExampleRateLimit() {
	run(&playhold.ServerConfig{
		RoomType: matchType(),
		// #region rate-limits
		// Twice the default rate of new connections per address, for players behind one shared address.
		ConnectRate: playhold.RateLimit{Every: 100 * time.Millisecond, Burst: 40},
		// The proxy in front already limits frames, so this layer is off; the server logs a warning at startup.
		MessageRate: playhold.RateLimit{Off: true},
		// GuestRate isn't set, so it keeps its default.
		// #endregion rate-limits
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
	}, &playhold.ListenConfig{})
}

// chat is a module with one message, a line of chat.
type chat struct{}

const msgSay uint32 = 1

func (chat) Name() string { return "chat" }

// #region module-limit
func (chat) Register(r *playhold.Registry) {
	playhold.Handle(r, msgSay, func(_ *playhold.RoomContext, _ *wrapperspb.StringValue) error {
		return nil // a real game would add the line to the room's state
	})
	// One line a second on average, with up to 5 at once, on top of ServerConfig.MessageRate.
	r.RateLimit(msgSay, playhold.RateLimit{Every: time.Second, Burst: 5})
}

// #endregion module-limit

func ExampleRegistry_RateLimit() {
	typ, err := playhold.NewRoomType(playhold.RoomConfig{Name: "match", Modules: []playhold.Module{chat{}}})
	if err != nil {
		log.Fatal(err)
	}
	run(&playhold.ServerConfig{RoomType: typ}, onTheInternet())
}

func ExampleAdmission() {
	run(&playhold.ServerConfig{
		RoomType: matchType(),
		// #region admission
		// Refuse new games sooner on a machine shared with other services; the other signals keep their defaults.
		Admission: playhold.Admission{Memory: 0.75},
		MaxRooms:  2_000,
		// #endregion admission
	}, onTheInternet())
}

func ExampleTimeout() {
	run(&playhold.ServerConfig{
		RoomType: matchType(),
		// #region timeouts
		// Players get five minutes to come back before the game hears they left.
		DisconnectGrace: playhold.Timeout{After: 5 * time.Minute},
		// Rooms with nobody connected stay open, and only InactiveRoomTimeout closes them; the server logs a warning.
		EmptyRoomTimeout: playhold.Timeout{Off: true},
		// #endregion timeouts
	}, onTheInternet())
}

func ExampleServerConfig_restartWhenStuck() {
	run(&playhold.ServerConfig{
		RoomType: matchType(),
		// #region restart-when-stuck
		// Only under a service manager that starts the server again, such as systemd with Restart=on-failure or a
		// container restart policy; otherwise the server stays down.
		RestartWhenStuck: true,
		// #endregion restart-when-stuck
	}, onTheInternet())
}

func ExampleRecordingConfig() {
	// #region recording
	typ, err := playhold.NewRoomType(playhold.RoomConfig{
		Name:   "match", // also the name of its recordings' folder
		Record: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	run(&playhold.ServerConfig{
		RoomType: typ,
		// On lasting storage, kept as private as the database: recordings hold players' messages.
		Recording: playhold.RecordingConfig{Dir: "/var/lib/game/recordings"},
	}, onTheInternet())
	// #endregion recording
}

func ExampleJournalConfig() {
	run(&playhold.ServerConfig{
		RoomType: matchType(),
		// #region crash-journal
		// On lasting storage, so it's still there after the crash. 100,000 entries keep about half a minute at 3,000
		// turns a second, in about 400 MB with the default 4 KiB each.
		CrashJournal: playhold.JournalConfig{Path: "/var/lib/game/journal", Entries: 100_000},
		// #endregion crash-journal
	}, onTheInternet())
}

// Package playhold is a self-hosted game backend built from authoritative rooms and composable modules.
//
// A [Runtime] runs rooms. Each room handles its inputs one at a time on its own goroutine, so game code needs no locks.
// A [RoomType] lists the [Module] values that give a room its state, message handlers, views and events, declared with
// [State], [Handle], [View] and [On]. Modules never import each other: one that needs another declares an interface,
// and the game passes it the provider when it builds the room type. Each player gets a view built for them alone, so a
// client never receives what its player may not see.
//
// A [Server], from [NewServer], lets clients connect as guests and join rooms by code, or be matched with others through
// ServerConfig.Matchmaking. [Server.ListenAndServe] serves it over TLS, with automatic or existing certificates, or
// behind a trusted proxy that ends TLS; ServerConfig.Development turns those checks off on a developer's machine. With
// ServerConfig.Store, such as [github.com/ny4rl4th0t3p/playhold/store/postgres], rooms are saved as they change and
// restored when the server starts, so a game survives a restart, a crash or a deploy. [Snapshot] says how a module's
// state is saved, and [Migrate] how a saved state moves to a new version. NewServer's example is a game's whole main
// function.
//
// [github.com/ny4rl4th0t3p/playhold/playholdtest] runs a game's room in a Go test, and replays recorded games. The frames
// clients and servers exchange are in [github.com/ny4rl4th0t3p/playhold/wire]. The client SDKs, for TypeScript and Go,
// are in repositories of their own, and the [documentation] has a walkthrough, a tutorial and guides.
//
// It is in early development, and the API can change.
//
// [documentation]: https://ny4rl4th0t3p.github.io/playhold/
package playhold

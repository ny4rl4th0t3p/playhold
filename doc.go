// Package playhold is a self-hosted game backend built from authoritative
// rooms and composable modules.
//
// A Runtime runs rooms. Each room handles its inputs one at a time on its own
// goroutine. A RoomType lists the Modules that give a room its state, message
// handlers and events.
//
// It is in early development: rooms and modules work, but players can't connect
// yet, and the API can change.
package playhold

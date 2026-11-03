// Package playhold is a self-hosted game backend built from authoritative
// rooms and composable modules.
//
// A Runtime runs rooms. Each room handles its inputs one at a time on its own
// goroutine. A RoomType lists the Modules that give a room its state, message
// handlers and events. A Server lets clients connect as guests and join rooms by
// code.
//
// It is in early development, and the API can change.
package playhold

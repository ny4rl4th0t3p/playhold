// Package wire is the frames a client and a playhold server exchange, generated from proto/playhold/v1/frames.proto.
// It's a module of its own, requiring only protobuf, so playhold and its Go SDK share one Go package for the frames: Go's
// protobuf registry is global to a program, and a program with both would otherwise register the same file twice.
package wire

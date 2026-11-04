// Package testonly uses playhold only in its tests, like a tool tested against a real server, so nothing here is
// flagged.
package testonly

import "time"

func clock() time.Time {
	return time.Now()
}

// Package unrelated doesn't import playhold, so nothing here is flagged.
package unrelated

import "time"

func clock(hands map[string]int) time.Time {
	for range hands {
	}
	return time.Now()
}

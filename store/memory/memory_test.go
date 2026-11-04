package memory

import (
	"testing"

	"github.com/ny4rl4th0t3p/playhold/internal/storetest"
	"github.com/ny4rl4th0t3p/playhold/store"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(_ *testing.T) store.Store { return New() })
}

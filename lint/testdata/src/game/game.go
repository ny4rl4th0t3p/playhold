package game

import (
	cryptorand "crypto/rand"
	"maps"
	"math/rand"
	randv2 "math/rand/v2"
	"slices"
	"time"

	"github.com/ny4rl4th0t3p/playhold"
)

var _ playhold.RoomContext

func clocks(start time.Time) {
	_ = time.Now()        // want `time.Now reads the real clock`
	_ = time.Since(start) // want `time.Since reads the real clock`
	_ = time.Until(start) // want `time.Until reads the real clock`

	clock := time.Now // want `time.Now reads the real clock`
	_ = clock
}

func clockValuesAreFine(start time.Time) time.Time {
	time.Sleep(time.Second)
	return start.Add(time.Hour)
}

func randomNumbers() {
	_ = rand.Intn(3)      // want `math/rand.Intn isn't reproducible`
	_ = randv2.IntN(3)    // want `math/rand/v2.IntN isn't reproducible`
	_ = cryptorand.Text() // want `crypto/rand.Text isn't reproducible`
}

// deal shows that using a generator handed to you, as rc.Rand gives you, is fine.
func deal(rng *randv2.Rand) int {
	return rng.IntN(3)
}

func maps1(hands map[string]int) {
	for range hands { // want `ranging over a map has a random order`
	}

	for player := range hands { // want `ranging over a map has a random order`
		_ = player
	}

	//playhold:order-independent
	for range hands {
	}

	for range hands { //playhold:order-independent
	}

	// A comment further up doesn't count.
	//playhold:order-independent

	for range hands { // want `ranging over a map has a random order`
	}

	for _, player := range slices.Sorted(maps.Keys(hands)) {
		_ = player
	}
}

type namedMap map[string]int

func namedMaps(hands namedMap) {
	for range hands { // want `ranging over a map has a random order`
	}
}

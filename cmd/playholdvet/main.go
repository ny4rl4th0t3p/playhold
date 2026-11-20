// Command playholdvet runs playhold's checks through go vet. Add it as a tool of the game's module, which keeps it at the
// same playhold version as the game, and run it from there:
//
//	go get -tool github.com/ny4rl4th0t3p/playhold/cmd/playholdvet
//	go vet -vettool=$(go tool -n playholdvet) ./...
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/ny4rl4th0t3p/playhold/lint"
)

func main() {
	unitchecker.Main(lint.Determinism)
}

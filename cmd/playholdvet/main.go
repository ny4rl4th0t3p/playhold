// Command playholdvet runs playhold's checks through go vet:
//
//	go build -o playholdvet github.com/ny4rl4th0t3p/playhold/cmd/playholdvet
//	go vet -vettool=./playholdvet ./...
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/ny4rl4th0t3p/playhold/lint"
)

func main() {
	unitchecker.Main(lint.Determinism)
}

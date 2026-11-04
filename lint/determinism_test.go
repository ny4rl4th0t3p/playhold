package lint_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/ny4rl4th0t3p/playhold/lint"
)

func TestDeterminism(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), lint.Determinism, "game", "unrelated", "testonly")
}

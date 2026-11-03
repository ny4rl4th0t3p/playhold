// Package lint holds checks for code built on playhold.
package lint

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const (
	playholdPath = "github.com/ny4rl4th0t3p/playhold"

	// orderIndependent on a map range, or on the line above it, says the iteration order doesn't affect results.
	orderIndependent = "//playhold:order-independent"
)

// Determinism flags code that would make a game impossible to reproduce: reading the real clock, using random
// numbers other than the room's, and ranging over maps, whose order is random. It checks packages that import
// playhold, and skips test files and generated files.
var Determinism = &analysis.Analyzer{
	Name: "playholddeterminism",
	Doc:  "flags real clocks, outside random numbers and map ranges in code built on playhold",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if !importsPlayhold(pass.Pkg) {
		return nil, nil
	}
	for _, file := range pass.Files {
		if strings.HasSuffix(pass.Fset.File(file.Pos()).Name(), "_test.go") || ast.IsGenerated(file) {
			continue
		}
		allowed := allowedLines(pass, file)
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.RangeStmt:
				checkRange(pass, node, allowed)
			case *ast.SelectorExpr:
				checkSelector(pass, node)
			}
			return true
		})
	}
	return nil, nil
}

func importsPlayhold(pkg *types.Package) bool {
	for _, imported := range pkg.Imports() {
		if imported.Path() == playholdPath {
			return true
		}
	}
	return false
}

func allowedLines(pass *analysis.Pass, file *ast.File) map[int]bool {
	lines := make(map[int]bool)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, orderIndependent) {
				lines[pass.Fset.Position(comment.Pos()).Line] = true
			}
		}
	}
	return lines
}

func checkRange(pass *analysis.Pass, node *ast.RangeStmt, allowed map[int]bool) {
	if _, isMap := pass.TypesInfo.TypeOf(node.X).Underlying().(*types.Map); !isMap {
		return
	}
	line := pass.Fset.Position(node.Pos()).Line
	if allowed[line] || allowed[line-1] {
		return
	}
	pass.Reportf(node.Pos(), "ranging over a map has a random order; sort the keys, or add %s if order can't matter", orderIndependent)
}

// checkSelector catches package functions whether they are called or passed around as values.
func checkSelector(pass *analysis.Pass, node *ast.SelectorExpr) {
	fn, ok := pass.TypesInfo.Uses[node.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
		return
	}
	switch fn.Pkg().Path() {
	case "time":
		switch fn.Name() {
		case "Now", "Since", "Until":
			pass.Reportf(node.Pos(), "time.%s reads the real clock; use rc.Now", fn.Name())
		}
	case "math/rand", "math/rand/v2", "crypto/rand":
		pass.Reportf(node.Pos(), "%s.%s isn't reproducible; use rc.Rand", fn.Pkg().Path(), fn.Name())
	}
}

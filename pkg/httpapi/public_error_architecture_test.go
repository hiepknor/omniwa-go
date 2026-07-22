package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInternalErrorsUsePublicBoundary prevents handlers from bypassing the
// centralized safe contract. Domain validation errors may still use their
// bounded public messages.
func TestInternalErrorsUsePublicBoundary(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "core") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall || len(call.Args) < 1 || !isJSONCall(call.Fun) || !isInternalStatus(call.Args[0]) {
				return true
			}
			position := fset.Position(call.Pos())
			t.Errorf("%s:%d writes HTTP 500 directly; use the httpapi public-error boundary", path, position.Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isJSONCall(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && (selector.Sel.Name == "JSON" || selector.Sel.Name == "AbortWithStatusJSON")
}

func isInternalStatus(expression ast.Expr) bool {
	if literal, ok := expression.(*ast.BasicLit); ok {
		return literal.Value == "500"
	}
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "StatusInternalServerError"
}

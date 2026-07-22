package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestDomainServicesDoNotOwnClientMaps locks the first migration boundary.
// The instance and whatsmeow lifecycle packages are a temporary allowlist until
// the follow-up slice removes the mirrored legacy maps.
func TestDomainServicesDoNotOwnClientMaps(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	pkgRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	allowed := map[string]bool{
		filepath.Join(pkgRoot, "instance", "service"):  true,
		filepath.Join(pkgRoot, "whatsmeow", "service"): true,
	}
	fset := token.NewFileSet()

	err := filepath.WalkDir(pkgRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed[filepath.Dir(path)] {
			return nil
		}
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		whatsmeowName := ""
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr == nil && importPath == "go.mau.fi/whatsmeow" {
				whatsmeowName = "whatsmeow"
				if imported.Name != nil {
					whatsmeowName = imported.Name.Name
				}
			}
		}
		if whatsmeowName == "" {
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			mapType, isMap := node.(*ast.MapType)
			if !isMap || !isStringType(mapType.Key) || !isWhatsmeowClientPointer(mapType.Value, whatsmeowName) {
				return true
			}
			position := fset.Position(mapType.Pos())
			t.Errorf("%s:%d owns a raw WhatsApp client map; depend on runtime.ClientProvider", path, position.Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isStringType(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == "string"
}

func isWhatsmeowClientPointer(expression ast.Expr, packageName string) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Client" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == packageName
}

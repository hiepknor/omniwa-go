package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/evolution-foundation/evolution-go/"

type sourceFile struct {
	path string
	rel  string
	set  *token.FileSet
	file *ast.File
}

func repositorySources(t *testing.T) []sourceFile {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	var sources []sourceFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || strings.HasSuffix(filepath.ToSlash(path), "/pkg/core") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sources = append(sources, sourceFile{path: path, rel: filepath.ToSlash(rel), set: set, file: parsed})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

func TestDependencyDirection(t *testing.T) {
	for _, source := range repositorySources(t) {
		layer := sourceLayer(source.rel)
		for _, spec := range source.file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.HasPrefix(importPath, modulePrefix) {
				continue
			}
			importedLayer := sourceLayer(strings.TrimPrefix(importPath, modulePrefix))
			forbidden := false
			switch layer {
			case "model":
				forbidden = importedLayer == "handler" || importedLayer == "service" || importedLayer == "repository" || importedLayer == "bootstrap" || importedLayer == "routes"
			case "repository":
				forbidden = importedLayer == "handler" || importedLayer == "service" || importedLayer == "routes"
			case "service":
				forbidden = importedLayer == "handler" || importedLayer == "routes"
			}
			if forbidden {
				t.Errorf("%s: %s layer must not import %s layer (%s)", source.rel, layer, importedLayer, importPath)
			}
		}
	}
}

func TestOutboundHTTPUsesNetguard(t *testing.T) {
	for _, source := range repositorySources(t) {
		if strings.HasPrefix(source.rel, "pkg/netguard/") {
			continue
		}
		httpAliases := importAliases(source.file, "net/http", "http")
		if len(httpAliases) == 0 {
			continue
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			if node == nil {
				return true
			}
			position := source.set.Position(node.Pos())
			switch value := node.(type) {
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok && isAliasSelector(selector, httpAliases) &&
					contains([]string{"Get", "Head", "Post", "PostForm"}, selector.Sel.Name) {
					t.Errorf("%s:%d: direct http.%s bypasses pkg/netguard", source.rel, position.Line, selector.Sel.Name)
				}
				if ident, ok := value.Fun.(*ast.Ident); ok && ident.Name == "new" && len(value.Args) == 1 && isHTTPType(value.Args[0], httpAliases, "Client", "Transport") {
					t.Errorf("%s:%d: constructing a raw HTTP client/transport bypasses pkg/netguard", source.rel, position.Line)
				}
			case *ast.CompositeLit:
				if isHTTPType(value.Type, httpAliases, "Client", "Transport") {
					t.Errorf("%s:%d: constructing a raw HTTP client/transport bypasses pkg/netguard", source.rel, position.Line)
				}
			case *ast.SelectorExpr:
				if isAliasSelector(value, httpAliases) && contains([]string{"DefaultClient", "DefaultTransport"}, value.Sel.Name) {
					t.Errorf("%s:%d: http.%s bypasses pkg/netguard", source.rel, position.Line, value.Sel.Name)
				}
			}
			return true
		})
	}
}

func TestSensitivePersistenceFieldsAreNotSerializable(t *testing.T) {
	for _, source := range repositorySources(t) {
		if !strings.Contains(source.rel, "/model/") {
			continue
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok || len(field.Names) == 0 {
				return true
			}
			for _, name := range field.Names {
				if !sensitiveFieldName(name.Name) {
					continue
				}
				jsonTag := ""
				if field.Tag != nil {
					unquoted, err := strconv.Unquote(field.Tag.Value)
					if err == nil {
						jsonTag = reflect.StructTag(unquoted).Get("json")
					}
				}
				if jsonTag != "-" {
					position := source.set.Position(field.Pos())
					t.Errorf("%s:%d: sensitive model field %s must use json:\"-\"", source.rel, position.Line, name.Name)
				}
			}
			return true
		})
	}
}

func TestWhatsAppRuntimeStateIsRegistryOwned(t *testing.T) {
	for _, source := range repositorySources(t) {
		whatsmeowAliases := importAliases(source.file, "go.mau.fi/whatsmeow", "whatsmeow")
		ast.Inspect(source.file, func(node ast.Node) bool {
			mapType, ok := node.(*ast.MapType)
			if !ok {
				return true
			}
			value := expressionString(mapType.Value)
			if isWhatsAppClientType(mapType.Value, whatsmeowAliases) || strings.Contains(value, "MyClient") {
				position := source.set.Position(mapType.Pos())
				t.Errorf("%s:%d: raw WhatsApp runtime maps are forbidden; use pkg/instance/runtime.Registry", source.rel, position.Line)
			}
			return true
		})
	}
}

func sourceLayer(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		switch part {
		case "model", "repository", "service", "handler", "bootstrap", "routes":
			return part
		}
	}
	return ""
}

func importAliases(file *ast.File, importPath, fallback string) map[string]struct{} {
	aliases := map[string]struct{}{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		alias := fallback
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias != "_" && alias != "." {
			aliases[alias] = struct{}{}
		}
	}
	return aliases
}

func isAliasSelector(selector *ast.SelectorExpr, aliases map[string]struct{}) bool {
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = aliases[ident.Name]
	return ok
}

func isHTTPType(expression ast.Expr, aliases map[string]struct{}, names ...string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && isAliasSelector(selector, aliases) && contains(names, selector.Sel.Name)
}

func sensitiveFieldName(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range []string{"token", "password", "secret", "qrcode", "proxy", "apikey", "credential", "accesskey", "privatekey"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func isWhatsAppClientType(expression ast.Expr, aliases map[string]struct{}) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Client" && isAliasSelector(selector, aliases)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func expressionString(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return "*" + expressionString(value.X)
	case *ast.SelectorExpr:
		return expressionString(value.X) + "." + value.Sel.Name
	case *ast.Ident:
		return value.Name
	case *ast.IndexExpr:
		return expressionString(value.X) + "[" + expressionString(value.Index) + "]"
	default:
		return fmt.Sprintf("%T", expression)
	}
}

func TestArchitectureClassifiers(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "pkg/instance/model/instance.go", want: "model"},
		{path: "pkg/group/repository/group.go", want: "repository"},
		{path: "pkg/bootstrap/supervisor.go", want: "bootstrap"},
	} {
		if got := sourceLayer(test.path); got != test.want {
			t.Fatalf("sourceLayer(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	for _, field := range []string{"Token", "TokenDigest", "ProxyPassword", "QRCode", "ClientSecret", "APIKey", "CredentialVersion"} {
		if !sensitiveFieldName(field) {
			t.Fatalf("sensitive field %q was not classified", field)
		}
	}
	if sensitiveFieldName("Connected") {
		t.Fatal("ordinary field was classified as sensitive")
	}
}

func TestHTTPGuardRecognizesAliasedRawClientConstruction(t *testing.T) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "fixture.go", `package fixture
import web "net/http"
var client = &web.Client{}
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := importAliases(file, "net/http", "http")
	detected := false
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if ok && isHTTPType(literal.Type, aliases, "Client", "Transport") {
			detected = true
		}
		return true
	})
	if !detected {
		t.Fatal("aliased raw HTTP client construction was not detected")
	}
}

func TestRuntimeGuardRecognizesAliasedWhatsAppClientMap(t *testing.T) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "fixture.go", `package fixture
import wa "go.mau.fi/whatsmeow"
var clients map[string]*wa.Client
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := importAliases(file, "go.mau.fi/whatsmeow", "whatsmeow")
	detected := false
	ast.Inspect(file, func(node ast.Node) bool {
		mapType, ok := node.(*ast.MapType)
		if ok && isWhatsAppClientType(mapType.Value, aliases) {
			detected = true
		}
		return true
	})
	if !detected {
		t.Fatal("aliased WhatsApp client map was not detected")
	}
}

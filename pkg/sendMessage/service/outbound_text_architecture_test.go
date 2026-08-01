package send_service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestTextSendBoundaryHasNoImplicitProviderRetry(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "send_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	methods := make(map[string]*ast.FuncDecl)
	for _, declaration := range parsed.Decls {
		if candidate, ok := declaration.(*ast.FuncDecl); ok {
			methods[candidate.Name.Name] = candidate
		}
	}
	if methods["sendTextWithRetry"] != nil {
		t.Fatal("legacy text provider retry boundary is still present")
	}
	for _, name := range []string{"SendText", "SendTextOnce"} {
		if methods[name] == nil || methods[name].Body == nil {
			t.Fatalf("%s method not found", name)
		}
	}
	providerCalls := 0
	ast.Inspect(methods["SendTextOnce"].Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			t.Error("SendTextOnce must not contain a retry loop")
		case *ast.CallExpr:
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "sendMessageContext" {
				providerCalls++
			}
		}
		return true
	})
	if providerCalls != 1 {
		t.Fatalf("SendTextOnce provider boundaries = %d, want 1", providerCalls)
	}
}

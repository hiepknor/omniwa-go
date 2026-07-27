package send_service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSendImageOnceHasOneUploadAndOneSendWithoutRetryLoop(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "send_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var method *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		candidate, ok := declaration.(*ast.FuncDecl)
		if ok && candidate.Name.Name == "SendImageOnce" {
			method = candidate
			break
		}
	}
	if method == nil || method.Body == nil {
		t.Fatal("SendImageOnce method not found")
	}
	uploadCalls, sendCalls := 0, 0
	ast.Inspect(method.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			t.Errorf("SendImageOnce must not contain a retry loop")
		case *ast.CallExpr:
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "Upload":
				uploadCalls++
			case "sendMessageContext":
				sendCalls++
			}
		}
		return true
	})
	if uploadCalls != 1 || sendCalls != 1 {
		t.Fatalf("SendImageOnce boundaries: upload=%d send=%d", uploadCalls, sendCalls)
	}
}

package logger

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
)

func TestRedactSensitive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		message string
		secret  string
	}{
		{name: "token label", message: "cache cleared for token: instance-secret", secret: "instance-secret"},
		{name: "JSON API key", message: `request {"apikey":"admin-secret","ok":true}`, secret: "admin-secret"},
		{name: "bearer authorization", message: "Authorization: Bearer signed.jwt.value", secret: "signed.jwt.value"},
		{name: "query signature", message: "https://media.example/file?X-Amz-Signature=signed-value&part=1", secret: "signed-value"},
		{name: "database password", message: "postgresql://postgres:database-secret@postgres:5432/omniwa", secret: "database-secret"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			redacted := RedactSensitive(test.message)
			if strings.Contains(redacted, test.secret) {
				t.Fatalf("redacted message still contains secret: %q", redacted)
			}
			if !strings.Contains(redacted, "[REDACTED]") {
				t.Fatalf("redacted message has no marker: %q", redacted)
			}
		})
	}
}

func TestLoggerRedactsPersistedMessage(t *testing.T) {
	directory := t.TempDir()
	manager := NewLoggerManager(&config.Config{
		LogDirectory: directory, LogMaxSize: 1, LogMaxBackups: 1, LogMaxAge: 1,
	})
	instanceLogger := manager.GetLogger("instance-a")
	instanceLogger.LogInfo("request token: %s", "instance-secret")
	if err := instanceLogger.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "instance-a", "instance.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "instance-secret") || !strings.Contains(string(contents), "[REDACTED]") {
		t.Fatalf("instance log was not redacted: %s", contents)
	}
}

func TestProductionLogCallsDoNotPassSensitiveModelFields(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve logger test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	forbidden := map[string]struct{}{
		"Token": {}, "Password": {}, "GlobalApiKey": {}, "MinioSecretKey": {}, "ApiAudioConverterKey": {},
	}
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "manager" || path == filepath.Join(repositoryRoot, "pkg", "core") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector || !strings.HasPrefix(selector.Sel.Name, "Log") {
				return true
			}
			for _, argument := range call.Args {
				ast.Inspect(argument, func(argumentNode ast.Node) bool {
					field, isField := argumentNode.(*ast.SelectorExpr)
					if !isField {
						return true
					}
					if _, blocked := forbidden[field.Sel.Name]; blocked {
						position := fileSet.Position(field.Pos())
						t.Errorf("sensitive field %s passed to %s at %s", field.Sel.Name, selector.Sel.Name, position)
					}
					return true
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

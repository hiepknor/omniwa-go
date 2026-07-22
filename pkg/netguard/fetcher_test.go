package netguard

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFetcherRejectsNonPublicTargets(t *testing.T) {
	t.Parallel()
	tests := []string{
		"http://127.0.0.1/file",
		"http://[::1]/file",
		"http://10.0.0.1/file",
		"http://172.16.0.1/file",
		"http://192.168.1.1/file",
		"http://169.254.169.254/latest/meta-data/",
	}
	f := mustTestFetcher(t, Settings{Policy: PolicyPublicOnly, Timeout: time.Second, MaxBytes: 1024}, net.DefaultResolver.LookupIPAddr, nil)
	for _, target := range tests {
		target := target
		t.Run(target, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), target)
			if !errors.Is(err, ErrUnsafeTarget) {
				t.Fatalf("Fetch() error = %v, want ErrUnsafeTarget", err)
			}
		})
	}
}

func TestFetcherRejectsPublicToPrivateRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1/internal")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	f := testServerFetcher(t, server, Settings{Policy: PolicyPublicOnly, Timeout: time.Second, MaxBytes: 1024})
	_, err := f.Fetch(context.Background(), "http://media.example/file")
	if !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Fetch() error = %v, want ErrUnsafeTarget", err)
	}
}

func TestFetcherTimesOutSlowResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	f := testServerFetcher(t, server, Settings{Policy: PolicyPublicOnly, Timeout: 25 * time.Millisecond, MaxBytes: 1024})
	_, err := f.Fetch(context.Background(), "http://media.example/file")
	if err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("Fetch() error = %v, want client timeout", err)
	}
}

func TestFetcherRejectsOversizedResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()

	f := testServerFetcher(t, server, Settings{Policy: PolicyPublicOnly, Timeout: time.Second, MaxBytes: 4})
	_, err := f.Fetch(context.Background(), "http://media.example/file")
	if !errors.Is(err, ErrResponseLarge) {
		t.Fatalf("Fetch() error = %v, want ErrResponseLarge", err)
	}
}

func TestFetcherPolicies(t *testing.T) {
	t.Parallel()
	disabled := mustTestFetcher(t, Settings{Policy: PolicyDisabled, Timeout: time.Second, MaxBytes: 1}, net.DefaultResolver.LookupIPAddr, nil)
	if _, err := disabled.Fetch(context.Background(), "http://example.com"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Fetch() error = %v, want ErrDisabled", err)
	}

	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	allowlist := mustTestFetcher(t, Settings{Policy: PolicyAllowlist, AllowedHosts: []string{"media.example"}, Timeout: time.Second, MaxBytes: 1}, resolve, nil)
	if _, err := allowlist.Fetch(context.Background(), "http://other.example/file"); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("allowlist Fetch() error = %v, want ErrUnsafeTarget", err)
	}
}

func TestUserControlledRemoteFetchesDoNotBypassGuard(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relativeDir := range []string{"pkg/group", "pkg/user", "pkg/sendMessage"} {
		dir := filepath.Join(repoRoot, relativeDir)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return walkErr
			}
			fileSet := token.NewFileSet()
			file, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, isHTTP := selector.X.(*ast.Ident)
				if !isHTTP || identifier.Name != "http" {
					return true
				}
				if selector.Sel.Name == "Get" || ((selector.Sel.Name == "NewRequest" || selector.Sel.Name == "NewRequestWithContext") && hasLiteralGET(call.Args)) {
					t.Errorf("unguarded HTTP GET in %s at line %d", path, fileSet.Position(call.Pos()).Line)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", relativeDir, err)
		}
	}
}

func TestProductionOutboundHTTPDoesNotBypassNetguard(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	root := filepath.Join(repoRoot, "pkg")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == filepath.Join(root, "netguard") || path == filepath.Join(root, "core") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CallExpr:
				selector, ok := typed.Fun.(*ast.SelectorExpr)
				identifier, isHTTP := selectorIdentifier(selector)
				if ok && isHTTP && identifier.Name == "http" && (selector.Sel.Name == "Get" || selector.Sel.Name == "Post" || selector.Sel.Name == "NewRequest" || selector.Sel.Name == "NewRequestWithContext") {
					t.Errorf("unguarded outbound HTTP call in %s at line %d", path, fileSet.Position(typed.Pos()).Line)
				}
			case *ast.CompositeLit:
				selector, ok := typed.Type.(*ast.SelectorExpr)
				identifier, isHTTP := selectorIdentifier(selector)
				if ok && isHTTP && identifier.Name == "http" && selector.Sel.Name == "Client" {
					t.Errorf("unguarded HTTP client in %s at line %d", path, fileSet.Position(typed.Pos()).Line)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production outbound HTTP: %v", err)
	}
}

func selectorIdentifier(selector *ast.SelectorExpr) (*ast.Ident, bool) {
	if selector == nil {
		return nil, false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return identifier, ok
}

func hasLiteralGET(arguments []ast.Expr) bool {
	for _, argument := range arguments {
		if literal, ok := argument.(*ast.BasicLit); ok && literal.Value == `"GET"` {
			return true
		}
		if selector, ok := argument.(*ast.SelectorExpr); ok && selector.Sel.Name == "MethodGet" {
			if identifier, ok := selector.X.(*ast.Ident); ok && identifier.Name == "http" {
				return true
			}
		}
	}
	return false
}

func testServerFetcher(t *testing.T, server *httptest.Server, settings Settings) *fetcher {
	t.Helper()
	serverAddress := server.Listener.Addr().String()
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}
	return mustTestFetcher(t, settings, resolve, dial)
}

func mustTestFetcher(t *testing.T, settings Settings, resolve resolverFunc, dial dialFunc) *fetcher {
	t.Helper()
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	f, err := newFetcher(settings, resolve, dial)
	if err != nil {
		t.Fatalf("newFetcher() error = %v", err)
	}
	return f
}

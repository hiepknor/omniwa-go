package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
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

func TestWhatsAppProviderMutationsUseFencedCommandFacade(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	configuration := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  root,
	}
	loaded, err := packages.Load(configuration, "./cmd/...", "./pkg/...")
	if err != nil {
		t.Fatal(err)
	}
	if count := packages.PrintErrors(loaded); count != 0 {
		t.Fatalf("type-check repository for provider command architecture: %d errors", count)
	}
	mutations := map[string]bool{
		"ConnectContext": true, "Logout": true, "PairPhone": true,
		"SendMessage": true, "Upload": true, "UploadNewsletter": true,
		"SendPresence": true, "SendChatPresence": true, "MarkRead": true, "SendAppState": true,
		"RejectCall": true, "SendPasskeyResponse": true, "SendPasskeyConfirmation": true,
		"CreateGroup": true, "LinkGroup": true, "UnlinkGroup": true,
		"SetGroupPhoto": true, "SetGroupName": true, "SetGroupTopic": true,
		"UpdateGroupParticipants": true, "UpdateGroupRequestParticipants": true,
		"JoinGroupWithLink": true, "LeaveGroup": true,
		"GetGroupInviteLink": true,
		"SetGroupAnnounce":   true, "SetGroupLocked": true,
		"SetGroupJoinApprovalMode": true, "SetGroupMemberAddMode": true,
		"SetPrivacySetting": true, "UpdateBlocklist": true, "SetStatusMessage": true,
		"CreateNewsletter": true, "NewsletterSubscribeLiveUpdates": true,
		"SetPassive": true, "SetDefaultDisappearingTimer": true,
		"SetForceActiveDeliveryReceipts": true, "SendUnavailableMessageRequest": true,
	}
	readOrLocalMethods := map[string]bool{
		"AddEventHandler": true, "RemoveEventHandler": true,
		"IsConnected": true, "IsLoggedIn": true, "Disconnect": true,
		"SetProxy": true, "SetProxyAddress": true,
		"BuildEdit": true, "BuildHistorySyncRequest": true, "BuildPollCreation": true, "BuildRevoke": true,
		"GenerateMessageID": true,
		"Download":          true, "DownloadToFile": true, "DownloadMediaWithPathToFile": true, "DecryptPollVote": true,
		"FetchAppState": true,
		"GetBlocklist":  true, "GetGroupInfo": true, "GetGroupRequestParticipants": true,
		"GetJoinedGroups": true, "GetNewsletterInfo": true, "GetNewsletterInfoWithInvite": true,
		"GetNewsletterMessages": true, "GetProfilePictureInfo": true, "GetSubscribedNewsletters": true,
		"GetUserInfo": true, "IsOnWhatsApp": true, "TryFetchPrivacySettings": true,
	}
	commandsChecked := 0
	for _, loadedPackage := range loaded {
		for fileIndex, file := range loadedPackage.Syntax {
			fileName := loadedPackage.CompiledGoFiles[fileIndex]
			var stack []ast.Node
			ast.Inspect(file, func(node ast.Node) bool {
				if node == nil {
					stack = stack[:len(stack)-1]
					return false
				}
				stack = append(stack, node)
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !isWhatsmeowClientMethod(loadedPackage, selector) {
					return true
				}
				isMutation := mutations[selector.Sel.Name]
				if selector.Sel.Name == "GetGroupInviteLink" {
					isMutation = callHasTrueReset(call)
					if !isMutation {
						return true
					}
				}
				if !isMutation && readOrLocalMethods[selector.Sel.Name] {
					return true
				}
				position := loadedPackage.Fset.Position(call.Pos())
				if !isMutation {
					t.Errorf("%s:%d: whatsmeow.Client.%s is not classified as a fenced mutation or an allowed read/local operation", fileName, position.Line, selector.Sel.Name)
					return true
				}
				commandsChecked++
				if !insideProviderCommandFacade(stack) {
					t.Errorf("%s:%d: whatsmeow.Client.%s bypasses the fenced provider command facade", fileName, position.Line, selector.Sel.Name)
					return true
				}
				if !callUsesCommandContext(call) {
					t.Errorf("%s:%d: whatsmeow.Client.%s does not use the bounded command context", fileName, position.Line, selector.Sel.Name)
				}
				return true
			})
		}
	}
	if commandsChecked < 50 {
		t.Fatalf("provider command architecture matched only %d mutation calls; type inventory is unexpectedly incomplete", commandsChecked)
	}
}

func isWhatsmeowClientMethod(pkg *packages.Package, selector *ast.SelectorExpr) bool {
	selection := pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return false
	}
	receiver := types.Unalias(selection.Recv())
	pointer, ok := receiver.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(pointer.Elem()).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "go.mau.fi/whatsmeow" && named.Obj().Name() == "Client"
}

func insideProviderCommandFacade(stack []ast.Node) bool {
	for index := len(stack) - 2; index > 0; index-- {
		literal, ok := stack[index].(*ast.FuncLit)
		if !ok {
			continue
		}
		call, ok := stack[index-1].(*ast.CallExpr)
		if !ok || !callContainsNode(call.Args, literal) {
			continue
		}
		name := calledFunctionName(call.Fun)
		return name == "DoProviderCommand" || name == "DoProviderCommandValue"
	}
	return false
}

func callContainsNode(arguments []ast.Expr, target ast.Expr) bool {
	for _, argument := range arguments {
		if argument == target {
			return true
		}
	}
	return false
}

func calledFunctionName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.IndexExpr:
		return calledFunctionName(value.X)
	case *ast.IndexListExpr:
		return calledFunctionName(value.X)
	default:
		return ""
	}
}

func callUsesCommandContext(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	identifier, ok := call.Args[0].(*ast.Ident)
	return ok && identifier.Name == "commandCtx"
}

func callHasTrueReset(call *ast.CallExpr) bool {
	if len(call.Args) < 3 {
		return false
	}
	identifier, ok := call.Args[2].(*ast.Ident)
	return ok && identifier.Name == "true"
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

func TestSensitiveConfigurationUsesFileBackedBoundary(t *testing.T) {
	sensitiveNames := map[string]struct{}{
		"POSTGRES_AUTH_DB": {}, "POSTGRES_USERS_DB": {}, "POSTGRES_PASSWORD": {},
		"GLOBAL_API_KEY": {}, "INSTANCE_TOKEN_HMAC_KEY": {}, "AMQP_URL": {},
		"WEBHOOK_URL": {}, "WEBHOOK_SIGNATURE_SECRET": {}, "API_AUDIO_CONVERTER_KEY": {}, "PROXY_PASSWORD": {},
		"NATS_URL": {}, "MEDIA_DESCRIPTOR_KEY": {}, "MINIO_ACCESS_KEY": {},
		"MINIO_SECRET_KEY": {},
	}
	for _, source := range repositorySources(t) {
		if source.rel == "pkg/config/secret.go" {
			continue
		}
		osAliases := importAliases(source.file, "os", "os")
		envAliases := importAliases(source.file, modulePrefix+"pkg/config/env", "env")
		if len(osAliases) == 0 {
			continue
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			function, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !isAliasSelector(function, osAliases) || !contains([]string{"Getenv", "LookupEnv"}, function.Sel.Name) {
				return true
			}
			name := ""
			switch argument := call.Args[0].(type) {
			case *ast.SelectorExpr:
				if isAliasSelector(argument, envAliases) {
					name = argument.Sel.Name
				}
			case *ast.BasicLit:
				if argument.Kind == token.STRING {
					name, _ = strconv.Unquote(argument.Value)
				}
			}
			if _, sensitive := sensitiveNames[name]; sensitive {
				position := source.set.Position(call.Pos())
				t.Errorf("%s:%d: %s must use pkg/config sensitiveValue for NAME_FILE support", source.rel, position.Line, name)
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

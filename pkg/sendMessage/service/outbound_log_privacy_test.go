package send_service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOutboundLogsDoNotIncludeRecipientOrProviderIdentifiers(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source location is unavailable")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "send_service.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`LogInfo("[%s] Number %s`,
		`LogInfo("[%s] List sent to %s`,
		`LogInfo("[%s] SendMessage called for number:`,
		`LogInfo("[%s] Recipient validated: %s`,
		`LogInfo("[%s] Sending message to %s`,
		`LogInfo("[%s] Message sent to %s`,
		`LogInfo("[%s] Building carousel for %s`,
		`LogInfo("[%s] Carousel sent to %s`,
		`using MediaHandle: %s`,
		`ServerID: %d`,
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("outbound log source contains privacy-sensitive format %q", forbidden)
		}
	}
}

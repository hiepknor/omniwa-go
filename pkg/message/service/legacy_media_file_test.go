package message_service

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestLegacyMediaSettingsHaveSafeDefaultsAndHardCap(t *testing.T) {
	service := NewMessageService(nil, nil, nil, LegacyMediaSettings{MaxBytes: legacyMediaHardMaxBytes + 1, Timeout: time.Second}, nil)
	configured := service.(*messageService)
	if configured.legacyMedia.MaxBytes != legacyMediaHardMaxBytes {
		t.Fatalf("max bytes = %d", configured.legacyMedia.MaxBytes)
	}
	service = NewMessageService(nil, nil, nil, LegacyMediaSettings{}, nil)
	configured = service.(*messageService)
	if configured.legacyMedia.MaxBytes != 32*1024*1024 || configured.legacyMedia.Timeout != 2*time.Minute {
		t.Fatalf("defaults = %+v", configured.legacyMedia)
	}
}

func TestLegacyBoundedFileRejectsOversizedWritesAndRemovesTempFile(t *testing.T) {
	file, err := newLegacyBoundedFile(4)
	if err != nil {
		t.Fatal(err)
	}
	path := file.path
	if _, err = file.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("5")); !errors.Is(err, errLegacyBoundExceeded) {
		t.Fatalf("oversized write error = %v", err)
	}
	if err = file.Truncate(5); !errors.Is(err, errLegacyBoundExceeded) {
		t.Fatalf("oversized truncate error = %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file remains after close: %v", err)
	}
}

func TestLegacyBoundedFileSupportsRandomAccess(t *testing.T) {
	file, err := newLegacyBoundedFile(8)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteAt([]byte("data"), 2); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err = io.ReadFull(file, got); err != nil || string(got) != "data" {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestLegacyDownloadableSelection(t *testing.T) {
	mime := "image/png"
	selected, gotMIME := legacyDownloadable(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: &mime}})
	if selected == nil || gotMIME != mime {
		t.Fatalf("selection = %T, %q", selected, gotMIME)
	}
	if selected, _ = legacyDownloadable(&waE2E.Message{}); selected != nil {
		t.Fatalf("unexpected selection %T", selected)
	}
}

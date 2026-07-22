package send_service

import (
	"errors"
	"testing"
)

func TestCappedBufferAcceptsDataWithinLimit(t *testing.T) {
	buffer := newCappedBuffer(5)

	written, err := buffer.Write([]byte("hello"))

	if err != nil || written != 5 || buffer.String() != "hello" {
		t.Fatalf("written=%d error=%v value=%q", written, err, buffer.String())
	}
}

func TestCappedBufferRejectsDataBeyondLimit(t *testing.T) {
	buffer := newCappedBuffer(5)

	written, err := buffer.Write([]byte("toolong"))

	if written != 5 || !errors.Is(err, errSubprocessOutputLimit) || buffer.Len() != 5 {
		t.Fatalf("written=%d error=%v length=%d", written, err, buffer.Len())
	}
	if written, err = buffer.Write([]byte("x")); written != 0 || !errors.Is(err, errSubprocessOutputLimit) {
		t.Fatalf("second write: written=%d error=%v", written, err)
	}
}

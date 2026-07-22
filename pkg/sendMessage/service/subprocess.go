package send_service

import (
	"bytes"
	"errors"
)

var errSubprocessOutputLimit = errors.New("subprocess output limit exceeded")

// cappedBuffer bounds memory consumed by subprocess output while retaining the
// bytes needed by the caller. Returning an error also stops os/exec from
// continuing to copy an unbounded stream.
type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func newCappedBuffer(limit int64) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining <= 0 {
		return 0, errSubprocessOutputLimit
	}
	if int64(len(data)) > remaining {
		written, _ := buffer.buffer.Write(data[:remaining])
		return written, errSubprocessOutputLimit
	}
	return buffer.buffer.Write(data)
}

func (buffer *cappedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (buffer *cappedBuffer) String() string {
	return buffer.buffer.String()
}

func (buffer *cappedBuffer) Len() int {
	return buffer.buffer.Len()
}

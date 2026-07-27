package message_service

import (
	"errors"
	"io"
	"os"
)

var errLegacyBoundExceeded = errors.New("legacy media download exceeded its bound")

type legacyBoundedFile struct {
	file  *os.File
	path  string
	limit int64
}

func newLegacyBoundedFile(limit int64) (*legacyBoundedFile, error) {
	file, err := os.CreateTemp("", "omniwa-legacy-media-*")
	if err != nil {
		return nil, err
	}
	return &legacyBoundedFile{file: file, path: file.Name(), limit: limit}, nil
}

func (f *legacyBoundedFile) Read(p []byte) (int, error)              { return f.file.Read(p) }
func (f *legacyBoundedFile) ReadAt(p []byte, off int64) (int, error) { return f.file.ReadAt(p, off) }
func (f *legacyBoundedFile) Seek(off int64, whence int) (int64, error) {
	return f.file.Seek(off, whence)
}
func (f *legacyBoundedFile) Stat() (os.FileInfo, error) { return f.file.Stat() }
func (f *legacyBoundedFile) Write(p []byte) (int, error) {
	position, err := f.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if position < 0 || position+int64(len(p)) > f.limit {
		return 0, errLegacyBoundExceeded
	}
	return f.file.Write(p)
}
func (f *legacyBoundedFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > f.limit {
		return 0, errLegacyBoundExceeded
	}
	return f.file.WriteAt(p, off)
}
func (f *legacyBoundedFile) Truncate(size int64) error {
	if size < 0 || size > f.limit {
		return errLegacyBoundExceeded
	}
	return f.file.Truncate(size)
}
func (f *legacyBoundedFile) Close() error {
	if f == nil || f.file == nil {
		return nil
	}
	return errors.Join(f.file.Close(), os.Remove(f.path))
}

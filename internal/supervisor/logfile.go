package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an io.Writer that appends to path and rotates it to
// path.1 ... path.N when it would exceed MaxBytes. Phones have small, slow
// storage, so unbounded logs are a real way to kill one.
type RotatingFile struct {
	path     string
	maxBytes int64
	maxFiles int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotating opens (creating if needed) a rotating log file.
func OpenRotating(path string, maxBytes int64, maxFiles int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write implements io.Writer.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	// Drop the oldest, shift the rest up: log.2 -> log.3, log.1 -> log.2, log -> log.1.
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.maxFiles))
	for i := r.maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if r.maxFiles >= 1 {
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			return err
		}
	} else {
		_ = os.Remove(r.path)
	}
	return r.open()
}

// Close closes the current file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

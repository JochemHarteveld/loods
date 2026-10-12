// Package store is the file machinery the plan, claim and run stores share:
// read a file only when it changed, and change it under an exclusive lock so
// the server, the CLI and several agents never lose each other's edits.
//
// What a file *means* stays in its own package (a codec plus whatever it does
// before writing, like pruning or validating); this package only knows how to
// read it when it moved, lock it, and replace it in one step.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Stamp identifies a file's contents cheaply: mtime and size. Empty when the
// file is not there.
func Stamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
}

// WriteAtomic replaces path in one step, so a reader never sees half a file.
func WriteAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), perm)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WithLock runs fn while holding an exclusive flock on a lock file next to
// path, across processes.
func WithLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// ReadError says the file itself could not be read or parsed, as opposed to a
// change being refused. Callers use it to add "fix it by hand first".
type ReadError struct {
	Path string
	Err  error
}

func (e *ReadError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *ReadError) Unwrap() error { return e.Err }

// Codec turns one file's bytes into the document a store works on and back.
// Empty is what a file that is not there yet decodes to, so every caller can
// treat a missing file as an empty one.
type Codec[T any] interface {
	Decode(b []byte) (T, error)
	Encode(v T) ([]byte, error)
	Empty() T
}

// File is one durable document: a cached copy of what is on disk, plus the
// locked read-change-write cycle. T is expected to be a pointer or a map, so
// an Update callback can change it in place.
type File[T any] struct {
	path  string
	codec Codec[T]

	mu    sync.Mutex
	stamp string
	doc   T
	err   error
}

func NewFile[T any](path string, codec Codec[T]) *File[T] {
	return &File[T]{path: path, codec: codec, doc: codec.Empty()}
}

func (f *File[T]) Path() string { return f.path }

// Refresh re-reads the file when it moved on disk and reports whether it did.
// A file that cannot be read keeps the last good document in memory and
// records the error: a broken file is not worth clearing the board for.
func (f *File[T]) Refresh() bool {
	stamp := Stamp(f.path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if stamp == f.stamp && f.stamp != "" {
		return false
	}
	doc, err := f.read()
	f.stamp, f.err = stamp, err
	if err != nil {
		return true
	}
	f.doc = doc
	return true
}

// Doc is the cached document plus the error of the last read.
func (f *File[T]) Doc() (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doc, f.err
}

func (f *File[T]) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// Update applies fn to the document on disk under the lock: re-read, change,
// write back, and pick the result up into the cache. fn may refuse by
// returning an error, in which case nothing is written.
func (f *File[T]) Update(fn func(T) error) error {
	err := WithLock(f.path, func() error {
		doc, err := f.read()
		if err != nil {
			return &ReadError{Path: f.path, Err: err}
		}
		if err := fn(doc); err != nil {
			return err
		}
		b, err := f.codec.Encode(doc)
		if err != nil {
			return err
		}
		return WriteAtomic(f.path, b, 0o644)
	})
	if err != nil {
		return err
	}
	f.Refresh()
	return nil
}

func (f *File[T]) read() (T, error) {
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return f.codec.Empty(), nil
	}
	if err != nil {
		return f.codec.Empty(), err
	}
	return f.codec.Decode(b)
}

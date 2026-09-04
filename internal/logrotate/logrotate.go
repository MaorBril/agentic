// Package logrotate provides a size-bounded log file.
//
// The router log had no ceiling: it was opened append-only and grew for as
// long as the install lived, reaching tens of megabytes and outgrowing the
// usage database it sat next to. A log nobody prunes is a slow disk leak, and
// the oldest lines are the least useful ones to keep.
package logrotate

import (
	"fmt"
	"os"
	"sync"
)

// Writer appends to a file and rotates it once it would exceed MaxBytes,
// keeping at most Keep older generations alongside it (path.1 … path.N).
// It is safe for concurrent use: slog handlers write from any goroutine.
type Writer struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	file *os.File
	size int64
}

// Open opens path for appending, rotating when a write would take it past
// maxBytes. keep is the number of older generations to retain; 0 keeps none,
// so the log simply restarts. An existing file that is already oversized
// rotates on its first write rather than being truncated, so nothing that has
// been written is thrown away silently.
func Open(path string, maxBytes int64, keep int) (*Writer, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("logrotate: maxBytes must be positive, got %d", maxBytes)
	}
	if keep < 0 {
		return nil, fmt.Errorf("logrotate: keep must not be negative, got %d", keep)
	}
	w := &Writer{path: path, maxBytes: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	size := int64(0)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	w.file, w.size = f, size
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	// Rotate before the write that would cross the line, so a single record
	// is never split across two generations. A record larger than the whole
	// budget still goes through in one piece — truncating a log line is worse
	// than briefly exceeding the ceiling.
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate closes the current file, shifts the generations along, and opens a
// fresh one. Another process holding the old descriptor keeps writing to the
// renamed file, which is the usual POSIX behaviour and harmless here: it
// stops when that process does.
func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	if w.keep == 0 {
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return w.open()
	}
	// Drop the oldest, then walk the rest down one slot.
	oldest := fmt.Sprintf("%s.%d", w.path, w.keep)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := w.keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", w.path, i)
		to := fmt.Sprintf("%s.%d", w.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

// Close releases the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

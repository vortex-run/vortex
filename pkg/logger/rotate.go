package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotateWriter is an io.Writer that writes to a log file and rotates it when it
// grows past MaxSizeMB or, via a background ticker, when rotated backups age
// out. It plugs directly into slog as a sink. It is safe for concurrent Write
// calls.
//
// Rotation renames the active file to "<path>.<timestamp>" (and gzips it when
// Compress is set), then opens a fresh file at the original path. Backups
// beyond MaxBackups, or older than MaxAgeDays, are removed.
type RotateWriter struct {
	Path       string
	MaxSizeMB  int  // rotate when the file exceeds this size; default 100
	MaxAgeDays int  // delete backups older than this; default 7
	MaxBackups int  // keep at most this many backups; default 5
	Compress   bool // gzip rotated files; default true

	// st holds the mutable runtime state behind a pointer so a RotateWriter
	// value (the constructor's config form) carries no lock and is safe to copy.
	st *rotateState
}

// rotateState is the writer's mutable runtime state, guarded by mu.
type rotateState struct {
	mu     sync.Mutex
	file   *os.File
	size   int64
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

// rotateTimeFormat is the timestamp suffix on rotated files; it is filesystem
// safe (no colons) and sorts chronologically.
const rotateTimeFormat = "2006-01-02T15-04-05"

// NewRotateWriter opens (creating if needed) the log file at cfg.Path, applies
// defaults for any zero fields, and starts a background goroutine that prunes
// aged-out backups hourly.
func NewRotateWriter(cfg RotateWriter) (*RotateWriter, error) {
	r := &RotateWriter{
		Path:       cfg.Path,
		MaxSizeMB:  cfg.MaxSizeMB,
		MaxAgeDays: cfg.MaxAgeDays,
		MaxBackups: cfg.MaxBackups,
		Compress:   cfg.Compress,
		st:         &rotateState{stop: make(chan struct{})},
	}
	if r.MaxSizeMB <= 0 {
		r.MaxSizeMB = 100
	}
	if r.MaxAgeDays <= 0 {
		r.MaxAgeDays = 7
	}
	if r.MaxBackups <= 0 {
		r.MaxBackups = 5
	}

	if dir := filepath.Dir(r.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating log directory: %w", err)
		}
	}
	if err := r.openFile(); err != nil {
		return nil, err
	}

	r.st.wg.Add(1)
	go r.ageLoop()
	return r, nil
}

// openFile opens the active log file in append mode and records its size.
func (r *RotateWriter) openFile() error {
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", r.Path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file %s: %w", r.Path, err)
	}
	r.st.file = f
	r.st.size = info.Size()
	return nil
}

// Write appends p to the current file, rotating immediately if the write pushes
// the file past MaxSizeMB.
func (r *RotateWriter) Write(p []byte) (int, error) {
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	if r.st.closed {
		return 0, os.ErrClosed
	}
	n, err := r.st.file.Write(p)
	r.st.size += int64(n)
	if err != nil {
		return n, err
	}
	if r.st.size >= int64(r.MaxSizeMB)*1024*1024 {
		if rerr := r.rotateLocked(); rerr != nil {
			return n, rerr
		}
	}
	return n, nil
}

// Rotate forces a rotation regardless of current size.
func (r *RotateWriter) Rotate() error {
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	if r.st.closed {
		return os.ErrClosed
	}
	return r.rotateLocked()
}

// rotateLocked performs the rotation; the caller must hold r.st.mu.
func (r *RotateWriter) rotateLocked() error {
	if err := r.st.file.Close(); err != nil {
		return fmt.Errorf("closing log file for rotation: %w", err)
	}
	backup := r.Path + "." + time.Now().Format(rotateTimeFormat) + ".log"
	if err := os.Rename(r.Path, backup); err != nil {
		// Re-open the original so logging can continue even if rename failed.
		_ = r.openFile()
		return fmt.Errorf("renaming log file: %w", err)
	}
	if err := r.openFile(); err != nil {
		return err
	}
	if r.Compress {
		r.st.wg.Add(1)
		go func() {
			defer r.st.wg.Done()
			_ = compressFile(backup)
			r.pruneBackups()
		}()
	} else {
		r.pruneBackups()
	}
	return nil
}

// backups returns existing backup files for this log, sorted oldest-first.
func (r *RotateWriter) backups() []string {
	matches, _ := filepath.Glob(r.Path + ".*")
	sort.Strings(matches) // timestamp format sorts chronologically
	return matches
}

// pruneBackups removes backups beyond MaxBackups and older than MaxAgeDays.
func (r *RotateWriter) pruneBackups() {
	files := r.backups()

	// Age-based removal.
	cutoff := time.Now().Add(-time.Duration(r.MaxAgeDays) * 24 * time.Hour)
	kept := files[:0]
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(f)
			continue
		}
		kept = append(kept, f)
	}

	// Count-based removal: drop the oldest until within MaxBackups.
	for len(kept) > r.MaxBackups {
		_ = os.Remove(kept[0])
		kept = kept[1:]
	}
}

// ageLoop prunes aged backups every hour until Close.
func (r *RotateWriter) ageLoop() {
	defer r.st.wg.Done()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-r.st.stop:
			return
		case <-t.C:
			r.st.mu.Lock()
			if !r.st.closed {
				r.pruneBackups()
			}
			r.st.mu.Unlock()
		}
	}
}

// Close flushes and closes the file and stops the background goroutine. It is
// idempotent.
func (r *RotateWriter) Close() error {
	r.st.mu.Lock()
	if r.st.closed {
		r.st.mu.Unlock()
		return nil
	}
	r.st.closed = true
	close(r.st.stop)
	err := r.st.file.Close()
	r.st.mu.Unlock()

	r.st.wg.Wait()
	return err
}

// compressFile gzips src to src+".gz" and removes the original on success.
func compressFile(src string) error {
	if strings.HasSuffix(src, ".gz") {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(src + ".gz")
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		_ = zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	_ = in.Close()
	return os.Remove(src)
}

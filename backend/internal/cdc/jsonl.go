package cdc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogFileName is the active CDC log under the data dir.
const LogFileName = "session-events.jsonl"

// DefaultMaxBytes is the size at which the log rotates (1 MiB).
const DefaultMaxBytes int64 = 1 << 20

// Log is the append-only JSONL sink the publisher writes to. When it grows past
// maxBytes it rotates by truncating in place and writing a reset marker as the
// new first line — the consumer treats a shrunken file as "resync from the DB
// snapshot", so the log itself is not the durable source of truth (SQLite is).
type Log struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

// OpenLog opens (creating if absent) the JSONL log in dir. maxBytes <= 0 uses
// DefaultMaxBytes.
func OpenLog(dir string, maxBytes int64) (*Log, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	path := filepath.Join(dir, LogFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open cdc log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat cdc log: %w", err)
	}
	return &Log{path: path, maxBytes: maxBytes, f: f, size: info.Size()}, nil
}

// Append writes one event as a JSON line, flushing to disk. It rotates first if
// the file is already at/over the size cap, so a single oversized burst still
// lands in a fresh segment.
func (l *Log) Append(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.size >= l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			return err
		}
	}
	return l.writeLocked(e)
}

func (l *Log) writeLocked(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal cdc line: %w", err)
	}
	line = append(line, '\n')
	n, err := l.f.Write(line)
	l.size += int64(n)
	if err != nil {
		return fmt.Errorf("write cdc line: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("sync cdc log: %w", err)
	}
	return nil
}

// rotateLocked renames the active file aside and starts a fresh one whose first
// line is a reset marker. Renaming (not truncating in place) gives the file a
// new identity, so a polling consumer reliably detects rotation via
// os.SameFile even if the fresh file grows past its old byte cursor between
// polls. The consumer then resyncs from the DB snapshot.
func (l *Log) rotateLocked() error {
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("close cdc log for rotate: %w", err)
	}
	archive := l.path + ".1"
	_ = os.Remove(archive) // best-effort: history lives in SQLite, not the log
	if err := os.Rename(l.path, archive); err != nil {
		return fmt.Errorf("rotate cdc log: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("reopen cdc log after rotate: %w", err)
	}
	l.f = f
	l.size = 0
	return l.writeLocked(resetMarker{Type: "reset", RotatedAt: time.Now().UTC()})
}

// Close closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

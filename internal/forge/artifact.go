package forge

import (
	"errors"
	"io"
	"os"
	"sync"
)

// SnapshotSource identifies how an exact-revision snapshot was obtained.
type SnapshotSource string

const (
	SnapshotSourceNative SnapshotSource = "native"
	SnapshotSourceReader SnapshotSource = "reader"
)

// SnapshotArtifact owns a private, file-backed snapshot. It deliberately does
// not expose its temporary path: callers receive random-access bytes and must
// close the artifact when they are done.
type SnapshotArtifact struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	source SnapshotSource
	closed bool
}

func NewSnapshotArtifact(source SnapshotSource) (*SnapshotArtifact, error) {
	file, err := os.CreateTemp("", "gew-snapshot-*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		return nil, err
	}
	return &SnapshotArtifact{file: file, path: file.Name(), source: source}, nil
}

func ArtifactFromBytes(data []byte, source SnapshotSource) (*SnapshotArtifact, error) {
	artifact, err := NewSnapshotArtifact(source)
	if err != nil {
		return nil, err
	}
	if _, err := artifact.Write(data); err != nil {
		_ = artifact.Close()
		return nil, err
	}
	return artifact, nil
}

func (a *SnapshotArtifact) Source() SnapshotSource { return a.source }

func (a *SnapshotArtifact) Size() int64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.file == nil {
		return 0
	}
	info, err := a.file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func (a *SnapshotArtifact) ReadAt(p []byte, off int64) (int, error) {
	if a == nil || a.file == nil {
		return 0, os.ErrClosed
	}
	return a.file.ReadAt(p, off)
}

func (a *SnapshotArtifact) Read(p []byte) (int, error) {
	if a == nil || a.file == nil {
		return 0, os.ErrClosed
	}
	return a.file.Read(p)
}

func (a *SnapshotArtifact) Write(p []byte) (int, error) {
	if a == nil || a.file == nil {
		return 0, os.ErrClosed
	}
	return a.file.Write(p)
}

func (a *SnapshotArtifact) Seek(offset int64, whence int) (int64, error) {
	if a == nil || a.file == nil {
		return 0, os.ErrClosed
	}
	return a.file.Seek(offset, whence)
}

func (a *SnapshotArtifact) Sync() error {
	if a == nil || a.file == nil {
		return os.ErrClosed
	}
	return a.file.Sync()
}

func (a *SnapshotArtifact) Bytes() ([]byte, error) {
	if a == nil {
		return nil, os.ErrClosed
	}
	if a.Size() > MaxRemoteSnapshot {
		return nil, errors.New("snapshot artifact exceeds the configured limit")
	}
	if _, err := a.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return ReadBounded(a, MaxRemoteSnapshot)
}

// Close is idempotent and removes the backing file. A close error and a
// cleanup error are both retained so callers can join them with a primary
// operation failure.
func (a *SnapshotArtifact) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	closeErr := a.file.Close()
	removeErr := os.Remove(a.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	a.file = nil
	a.path = ""
	return errors.Join(closeErr, removeErr)
}

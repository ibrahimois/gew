package forge

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const maxRemoteSnapshot = MaxRemoteSnapshot

type SnapshotResult struct {
	Artifact *SnapshotArtifact
	Files    map[string]RemoteFile
}

func (r SnapshotResult) Close() error {
	if r.Artifact == nil {
		return nil
	}
	return r.Artifact.Close()
}

func Snapshot(ctx context.Context, remote Forge, ref RepositoryRef, revision string) (*SnapshotArtifact, error) {
	result, err := SnapshotWithTree(ctx, remote, ref, revision)
	if err != nil {
		return nil, err
	}
	return result.Artifact, nil
}

// SnapshotWithTree returns an owned exact-revision artifact. A nil Files map
// means the provider-native archive fast path succeeded.
func SnapshotWithTree(ctx context.Context, remote Forge, ref RepositoryRef, revision string) (SnapshotResult, error) {
	if snapshotter, ok := remote.(ForgeSnapshotter); ok {
		artifact, nativeErr := snapshotter.Snapshot(ctx, ref, revision)
		return snapshotWithNativeResult(ctx, remote, ref, revision, artifact, nativeErr)
	}
	artifact, files, err := readerSnapshot(ctx, remote, ref, revision)
	return SnapshotResult{Artifact: artifact, Files: files}, err
}

func snapshotWithNativeResult(ctx context.Context, remote Forge, ref RepositoryRef, revision string, artifact *SnapshotArtifact, nativeErr error) (SnapshotResult, error) {
	if nativeErr == nil {
		if artifact == nil {
			return SnapshotResult{}, errors.New("native snapshot returned no artifact")
		}
		return SnapshotResult{Artifact: artifact}, nil
	}
	if artifact != nil {
		_ = artifact.Close()
	}
	if ctx.Err() != nil {
		return SnapshotResult{}, errors.Join(fmt.Errorf("native snapshot: %w", nativeErr), ctx.Err())
	}
	fallback, files, fallbackErr := readerSnapshot(ctx, remote, ref, revision)
	if fallbackErr != nil {
		return SnapshotResult{}, errors.Join(fmt.Errorf("native snapshot: %w", nativeErr), fmt.Errorf("tree/blob snapshot: %w", fallbackErr))
	}
	return SnapshotResult{Artifact: fallback, Files: files}, nil
}

func readerSnapshot(ctx context.Context, remote RepositoryReader, ref RepositoryRef, revision string) (*SnapshotArtifact, map[string]RemoteFile, error) {
	files, err := remote.Tree(ctx, ref, revision)
	if err != nil {
		return nil, nil, err
	}
	concurrency := DefaultReadConcurrency
	if full, ok := remote.(Forge); ok && full.Capabilities().ReadConcurrency > 0 {
		concurrency = full.Capabilities().ReadConcurrency
	}
	artifact, err := readerSnapshotFromTree(ctx, remote, ref, revision, files, concurrency)
	return artifact, files, err
}

func readerSnapshotFromTree(ctx context.Context, remote RepositoryReader, ref RepositoryRef, revision string, files map[string]RemoteFile, concurrency int) (*SnapshotArtifact, error) {
	paths := make([]string, 0, len(files))
	for filePath, file := range files {
		cleaned, err := ValidateRemotePath(filePath)
		if err != nil {
			return nil, err
		}
		if cleaned != filePath {
			return nil, fmt.Errorf("repository tree returned non-canonical path %q", filePath)
		}
		objectType := file.Mode & 0o170000
		if objectType == 0o120000 || objectType == 0o160000 {
			return nil, fmt.Errorf("repository contains unsupported object at %s", cleaned)
		}
		paths = append(paths, cleaned)
	}
	sort.Strings(paths)

	repository := strings.TrimSpace(ref.Name)
	if repository == "" {
		repository = "repository"
	}
	root, err := ValidateRemotePath(repository + "-" + revision)
	if err != nil || strings.Contains(root, "/") {
		return nil, fmt.Errorf("unsafe snapshot root for repository %q at revision %q", ref.Name, revision)
	}

	blobs, err := ReadBlobBatch(ctx, remote, ref, files, concurrency)
	if err != nil {
		return nil, err
	}
	defer CloseArtifacts(blobs)
	artifact, err := NewSnapshotArtifact(SnapshotSourceReader)
	if err != nil {
		return nil, err
	}
	writer := zip.NewWriter(artifact)
	closeWith := func(primary error) error {
		return errors.Join(primary, writer.Close())
	}
	for _, filePath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(closeWith(err), artifact.Close())
		}
		file := files[filePath]
		header := &zip.FileHeader{Name: path.Join(root, filePath), Method: zip.Deflate}
		mode := uint32(0o644)
		if file.Mode&0o111 != 0 {
			mode = 0o755
		}
		header.SetMode(os.FileMode(mode))
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, errors.Join(closeWith(err), artifact.Close())
		}
		if _, err := blobs[filePath].Seek(0, io.SeekStart); err != nil {
			return nil, errors.Join(closeWith(err), artifact.Close())
		}
		if _, err := io.Copy(entry, blobs[filePath]); err != nil {
			return nil, errors.Join(closeWith(err), artifact.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return nil, errors.Join(err, artifact.Close())
	}
	if artifact.Size() > maxRemoteSnapshot {
		return nil, errors.Join(fmt.Errorf("remote snapshot exceeds %d bytes", maxRemoteSnapshot), artifact.Close())
	}
	if err := artifact.Sync(); err != nil {
		return nil, errors.Join(err, artifact.Close())
	}
	return artifact, nil
}

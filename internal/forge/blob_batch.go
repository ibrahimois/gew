package forge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

const (
	DefaultReadConcurrency = 4
	MaxReadConcurrency     = 16
)

// ReadBlobBatch fetches independent exact-revision blobs with bounded
// concurrency and spools each result into an owned artifact. On failure it
// closes every artifact produced by the batch.
func ReadBlobBatch(ctx context.Context, remote RepositoryReader, ref RepositoryRef, files map[string]RemoteFile, concurrency int) (map[string]*SnapshotArtifact, error) {
	if concurrency <= 0 {
		concurrency = DefaultReadConcurrency
	}
	if concurrency > MaxReadConcurrency {
		return nil, fmt.Errorf("read concurrency %d exceeds maximum %d", concurrency, MaxReadConcurrency)
	}
	paths := make([]string, 0, len(files))
	var declared int64
	for filePath, metadata := range files {
		cleaned, err := ValidateRemotePath(filePath)
		if err != nil || cleaned != filePath {
			if err == nil {
				err = fmt.Errorf("repository tree returned non-canonical path %q", filePath)
			}
			return nil, err
		}
		if metadata.Size > 0 {
			if metadata.Size > MaxRemoteSnapshot-declared {
				return nil, fmt.Errorf("remote snapshot exceeds %d bytes", MaxRemoteSnapshot)
			}
			declared += metadata.Size
		}
		paths = append(paths, filePath)
	}
	sort.Strings(paths)

	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	results := make(map[string]*SnapshotArtifact, len(paths))
	var mu sync.Mutex
	var firstErr error
	var actual int64
	worker := func() {
		for filePath := range jobs {
			if batchCtx.Err() != nil {
				return
			}
			content, err := remote.Blob(batchCtx, ref, files[filePath])
			if err == nil {
				mu.Lock()
				if int64(len(content)) > MaxRemoteSnapshot-actual {
					err = fmt.Errorf("remote snapshot exceeds %d bytes", MaxRemoteSnapshot)
				} else {
					actual += int64(len(content))
				}
				mu.Unlock()
			}
			var artifact *SnapshotArtifact
			if err == nil {
				artifact, err = ArtifactFromBytes(content, SnapshotSourceReader)
			}
			content = nil
			mu.Lock()
			if err != nil && firstErr == nil {
				firstErr = err
				cancel()
			}
			if err == nil {
				results[filePath] = artifact
			}
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}
	workers := concurrency
	if workers > len(paths) {
		workers = len(paths)
	}
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		go func() { defer group.Done(); worker() }()
	}
	for _, filePath := range paths {
		select {
		case jobs <- filePath:
		case <-batchCtx.Done():
			break
		}
		if batchCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	group.Wait()
	if firstErr == nil && ctx.Err() != nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		var closeErr error
		for _, artifact := range results {
			closeErr = errors.Join(closeErr, artifact.Close())
		}
		return nil, errors.Join(firstErr, closeErr)
	}
	return results, nil
}

func CloseArtifacts(artifacts map[string]*SnapshotArtifact) error {
	var result error
	for _, artifact := range artifacts {
		result = errors.Join(result, artifact.Close())
	}
	return result
}

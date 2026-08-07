package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

func forgeSnapshot(ctx context.Context, remote Forge, ref RepositoryRef, revision string) ([]byte, error) {
	if snapshotter, ok := remote.(ForgeSnapshotter); ok {
		return snapshotter.Snapshot(ctx, ref, revision)
	}
	return readerSnapshot(ctx, remote, ref, revision)
}

func readerSnapshot(ctx context.Context, remote RepositoryReader, ref RepositoryRef, revision string) ([]byte, error) {
	files, err := remote.Tree(ctx, ref, revision)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	var declaredSize int64
	for filePath, file := range files {
		cleaned, err := validateRemotePath(filePath)
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
		if file.Size > 0 {
			if file.Size > maxRemoteSnapshot-declaredSize {
				return nil, fmt.Errorf("remote snapshot exceeds %d bytes", maxRemoteSnapshot)
			}
			declaredSize += file.Size
		}
		paths = append(paths, cleaned)
	}
	sort.Strings(paths)

	repository := strings.TrimSpace(ref.Name)
	if repository == "" {
		repository = "repository"
	}
	root, err := validateRemotePath(repository + "-" + revision)
	if err != nil || strings.Contains(root, "/") {
		return nil, fmt.Errorf("unsafe snapshot root for repository %q at revision %q", ref.Name, revision)
	}

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	closed := false
	closeWriter := func(primary error) error {
		if closed {
			return primary
		}
		closed = true
		if closeErr := writer.Close(); closeErr != nil {
			if primary != nil {
				return errors.Join(primary, closeErr)
			}
			return closeErr
		}
		return primary
	}

	var actualSize int64
	for _, filePath := range paths {
		file := files[filePath]
		content, err := remote.Blob(ctx, ref, file)
		if err != nil {
			return nil, closeWriter(err)
		}
		if int64(len(content)) > maxRemoteSnapshot-actualSize {
			return nil, closeWriter(fmt.Errorf("remote snapshot exceeds %d bytes", maxRemoteSnapshot))
		}
		actualSize += int64(len(content))
		header := &zip.FileHeader{Name: path.Join(root, filePath), Method: zip.Deflate}
		mode := uint32(0o644)
		if file.Mode&0o111 != 0 {
			mode = 0o755
		}
		header.SetMode(os.FileMode(mode))
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, closeWriter(err)
		}
		if _, err := entry.Write(content); err != nil {
			return nil, closeWriter(err)
		}
	}
	if err := closeWriter(nil); err != nil {
		return nil, err
	}
	if int64(output.Len()) > maxRemoteSnapshot {
		return nil, fmt.Errorf("remote snapshot exceeds %d bytes", maxRemoteSnapshot)
	}
	return output.Bytes(), nil
}

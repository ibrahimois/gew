package forge

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

type snapshotForge struct {
	files   map[string]RemoteFile
	blobs   map[string][]byte
	blobErr error
}

func (f *snapshotForge) Kind() ForgeKind                 { return ForgeKind("snapshot") }
func (f *snapshotForge) Capabilities() ForgeCapabilities { return ForgeCapabilities{} }
func (f *snapshotForge) Probe(context.Context) error     { return nil }
func (f *snapshotForge) ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error) {
	return RepositoryRef{}, RepositoryInfo{}, nil
}
func (f *snapshotForge) Head(context.Context, RepositoryRef, string) (string, error) {
	return "revision", nil
}
func (f *snapshotForge) Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error) {
	return f.files, nil
}
func (f *snapshotForge) Blob(_ context.Context, _ RepositoryRef, file RemoteFile) ([]byte, error) {
	if f.blobErr != nil {
		return nil, f.blobErr
	}
	return append([]byte(nil), f.blobs[file.BlobID]...), nil
}

func TestForgeSnapshotFallbackContract(t *testing.T) {
	remote := &snapshotForge{
		files: map[string]RemoteFile{
			"z.bin":      {BlobID: "z", Mode: 0o100644, Size: 3},
			"bin/run.sh": {BlobID: "run", Mode: 0o100755, Size: 10},
		},
		blobs: map[string][]byte{"z": {'a', 0, 'b'}, "run": []byte("#!/bin/sh\n")},
	}
	first, err := Snapshot(context.Background(), remote, RepositoryRef{Name: "demo"}, "revision")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Snapshot(context.Background(), remote, RepositoryRef{Name: "demo"}, "revision")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fallback snapshot is not deterministic")
	}
	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{reader.File[0].Name, reader.File[1].Name}; !reflect.DeepEqual(got, []string{"demo-revision/bin/run.sh", "demo-revision/z.bin"}) {
		t.Fatalf("entry order = %#v", got)
	}
	if reader.File[0].Mode().Perm() != 0o755 || reader.File[1].Mode().Perm() != 0o644 {
		t.Fatalf("entry modes = %o, %o", reader.File[0].Mode().Perm(), reader.File[1].Mode().Perm())
	}
	entry, _ := reader.File[1].Open()
	content, _ := io.ReadAll(entry)
	entry.Close()
	if !bytes.Equal(content, []byte{'a', 0, 'b'}) {
		t.Fatalf("binary content = %v", content)
	}
}

func TestForgeSnapshotFallbackRejectsUnsafeObjectsFailuresAndSize(t *testing.T) {
	tests := []struct {
		name   string
		remote *snapshotForge
	}{
		{name: "unsafe path", remote: &snapshotForge{files: map[string]RemoteFile{"../escape": {BlobID: "x"}}}},
		{name: "symlink", remote: &snapshotForge{files: map[string]RemoteFile{"link": {BlobID: "x", Mode: 0o120000}}}},
		{name: "submodule", remote: &snapshotForge{files: map[string]RemoteFile{"module": {BlobID: "x", Mode: 0o160000}}}},
		{name: "declared size", remote: &snapshotForge{files: map[string]RemoteFile{"huge": {BlobID: "x", Size: maxRemoteSnapshot + 1}}}},
		{name: "blob failure", remote: &snapshotForge{files: map[string]RemoteFile{"file": {BlobID: "x"}}, blobErr: errors.New("blob failed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Snapshot(context.Background(), test.remote, RepositoryRef{Name: "demo"}, "revision"); err == nil {
				t.Fatal("invalid fallback snapshot was accepted")
			}
		})
	}
}

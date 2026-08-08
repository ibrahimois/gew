package forge

import (
	"os"
	"testing"
)

func TestSnapshotArtifactOwnsAndRemovesPrivateFile(t *testing.T) {
	artifact, err := ArtifactFromBytes([]byte("snapshot"), SnapshotSourceNative)
	if err != nil {
		t.Fatal(err)
	}
	path := artifact.path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || artifact.Size() != 8 || artifact.Source() != SnapshotSourceNative {
		t.Fatalf("artifact mode=%o size=%d source=%q", info.Mode().Perm(), artifact.Size(), artifact.Source())
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("backing file remains: %v", err)
	}
}

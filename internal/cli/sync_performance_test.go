package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeltaPullReadsOnlyChangedPaths(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	fake.mu.Lock()
	fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls = 0, 0, 0, 0
	fake.files["README.md"] = []byte("changed remotely\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	if err != nil || string(content) != "changed remotely\n" {
		t.Fatalf("README = %q, %v", content, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.headCalls != 1 || fake.treeCalls != 1 || fake.blobCalls != 1 || fake.snapshotCalls != 0 {
		t.Fatalf("requests: head=%d tree=%d blob=%d snapshot=%d", fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls)
	}
}

func TestPullUpToDateDoesNotScanUnsupportedLocalContent(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	if err := os.Symlink("README.md", filepath.Join(checkout, "local-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fake.mu.Lock()
	fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls = 0, 0, 0, 0
	fake.mu.Unlock()
	if err := a.pull(nil); err != nil {
		t.Fatalf("unchanged pull scanned local content: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.headCalls != 1 || fake.treeCalls != 0 || fake.blobCalls != 0 || fake.snapshotCalls != 0 {
		t.Fatalf("requests: head=%d tree=%d blob=%d snapshot=%d", fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls)
	}
}

func TestPushProofAvoidsPostCommitTreeAndSnapshot(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("local push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "change readme"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls = 0, 0, 0, 0
	fake.mu.Unlock()
	if err := a.push(nil); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.headCalls != 1 || fake.treeCalls != 1 || fake.blobCalls != 1 || fake.snapshotCalls != 0 {
		t.Fatalf("requests: head=%d tree=%d blob=%d snapshot=%d", fake.headCalls, fake.treeCalls, fake.blobCalls, fake.snapshotCalls)
	}
}

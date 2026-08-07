package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
)

type gitExportForge struct {
	head       string
	files      map[string][]byte
	applyCount int
	failApply  error
	loseResult bool
	commits    map[string]RemoteCommit
}

func (f *gitExportForge) Kind() ForgeKind { return ForgeGitea }
func (f *gitExportForge) Capabilities() ForgeCapabilities {
	return ForgeCapabilities{Push: true, BranchCreate: true}
}
func (f *gitExportForge) Probe(context.Context) error { return nil }
func (f *gitExportForge) ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error) {
	return RepositoryRef{Forge: ForgeGitea}, RepositoryInfo{}, nil
}
func (f *gitExportForge) Head(context.Context, RepositoryRef, string) (string, error) {
	if f.head == "" {
		return "", ErrNotFound
	}
	return f.head, nil
}
func (f *gitExportForge) Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error) {
	result := make(map[string]RemoteFile, len(f.files))
	for filePath, content := range f.files {
		result[filePath] = RemoteFile{BlobID: filePath, Size: int64(len(content)), Mode: 0o100644}
	}
	return result, nil
}
func (f *gitExportForge) Blob(_ context.Context, _ RepositoryRef, file RemoteFile) ([]byte, error) {
	return append([]byte(nil), f.files[file.BlobID]...), nil
}
func (f *gitExportForge) ApplyCommit(_ context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if f.failApply != nil {
		return ApplyCommitResult{}, f.failApply
	}
	if request.ExpectedHead != f.head {
		return ApplyCommitResult{}, ErrStaleHead
	}
	previous := f.head
	for _, change := range request.Changes {
		switch change.Operation {
		case "deleted", "delete":
			delete(f.files, change.Path)
		default:
			f.files[change.Path] = append([]byte(nil), change.Content...)
		}
	}
	f.applyCount++
	f.head = fmt.Sprintf("provider-%d", f.applyCount)
	parents := []string(nil)
	if previous != "" {
		parents = []string{previous}
	}
	paths := make([]string, len(request.Changes))
	for index, change := range request.Changes {
		paths[index] = change.Path
	}
	if f.commits == nil {
		f.commits = make(map[string]RemoteCommit)
	}
	f.commits[f.head] = RemoteCommit{ID: f.head, Message: request.Message, ParentIDs: parents, Paths: paths}
	if f.loseResult {
		f.loseResult = false
		return ApplyCommitResult{}, errors.New("connection reset after request transmission")
	}
	return ApplyCommitResult{CommitID: f.head, ParentIDs: parents, ConditionalRef: true}, nil
}

func (f *gitExportForge) CommitDetails(_ context.Context, _ RepositoryRef, commit string) (RemoteCommit, error) {
	details, ok := f.commits[commit]
	if !ok {
		return RemoteCommit{}, ErrNotFound
	}
	return details, nil
}

func TestGitExportMapsDifferentIDsAndAdvancesTrackingAfterVerification(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := a.gitCommit(root, state, "change", "", ""); err != nil {
		t.Fatal(err)
	}
	forge := &gitExportForge{head: "remote-base", files: map[string][]byte{"README.md": []byte("base\n")}}
	if err := a.gitPushWithForge(context.Background(), root, state, "", forge); err != nil {
		t.Fatal(err)
	}
	if forge.applyCount != 1 || forge.head != "provider-1" || string(forge.files["README.md"]) != "changed\n" {
		t.Fatalf("forge = %#v", forge)
	}
	_, updated, err := readWorkspaceAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if updated.BaseCommit != "provider-1" || updated.Hybrid.LastProviderID != "provider-1" || updated.Hybrid.LastLocalOID == "provider-1" {
		t.Fatalf("updated state = %#v", updated)
	}
	if _, err := os.Stat(receiptPath(root, updated.Hybrid.LastLocalOID)); err != nil {
		t.Fatal(err)
	}
	repository, _ := git.PlainOpen(root)
	pending, err := pendingGitCommits(repository, updated.Hybrid.TrackingRef)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}

func TestGitExportStaleHeadDoesNotWriteOrAdvance(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := a.gitCommit(root, state, "change", "", ""); err != nil {
		t.Fatal(err)
	}
	forge := &gitExportForge{head: "advanced", files: map[string][]byte{"README.md": []byte("remote\n")}}
	err := a.gitPushWithForge(context.Background(), root, state, "", forge)
	if err == nil || !strings.Contains(err.Error(), "advanced") || forge.applyCount != 0 {
		t.Fatalf("push error/count = %v/%d", err, forge.applyCount)
	}
	repository, _ := git.PlainOpen(root)
	pending, _ := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if len(pending) != 1 {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestGitExportAmbiguousAcceptedResponseReconcilesWithoutDuplicate(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := a.gitCommit(root, state, "change", "", ""); err != nil {
		t.Fatal(err)
	}
	forge := &gitExportForge{head: "remote-base", files: map[string][]byte{"README.md": []byte("base\n")}, loseResult: true}
	if err := a.gitPushWithForge(context.Background(), root, state, "", forge); err == nil || forge.applyCount != 1 {
		t.Fatalf("ambiguous first push = %v, count %d", err, forge.applyCount)
	}
	if _, err := os.Stat(exportPreparedPath(root)); err != nil {
		t.Fatal(err)
	}
	if err := a.gitPushWithForge(context.Background(), root, state, "", forge); err != nil {
		t.Fatal(err)
	}
	if forge.applyCount != 1 {
		t.Fatalf("reconciliation duplicated remote commit: %d", forge.applyCount)
	}
	if _, err := os.Stat(exportPreparedPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared journal remains: %v", err)
	}
}

func readWorkspaceAt(root string) (string, workspaceState, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gew", "state.json"))
	if err != nil {
		return "", workspaceState{}, err
	}
	var state workspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		return "", workspaceState{}, err
	}
	return root, state, nil
}

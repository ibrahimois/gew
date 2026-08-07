package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
)

type migrationForge struct {
	head    string
	content map[string][]byte
}

func (f migrationForge) Kind() ForgeKind                 { return ForgeGitea }
func (f migrationForge) Capabilities() ForgeCapabilities { return ForgeCapabilities{} }
func (f migrationForge) Probe(context.Context) error     { return nil }
func (f migrationForge) ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error) {
	return RepositoryRef{Forge: ForgeGitea}, RepositoryInfo{}, nil
}
func (f migrationForge) Head(context.Context, RepositoryRef, string) (string, error) {
	if f.head == "" {
		return "", ErrNotFound
	}
	return f.head, nil
}
func (f migrationForge) Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error) {
	result := make(map[string]RemoteFile, len(f.content))
	for filePath, content := range f.content {
		result[filePath] = RemoteFile{BlobID: filePath, Size: int64(len(content)), Mode: 0o100644}
	}
	return result, nil
}
func (f migrationForge) Blob(_ context.Context, _ RepositoryRef, file RemoteFile) ([]byte, error) {
	content, ok := f.content[file.BlobID]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), content...), nil
}
func makeMigrationWorkspace(t *testing.T) (string, workspaceState, migrationForge) {
	t.Helper()
	root := t.TempDir()
	base := []byte("base\n")
	changed := []byte("changed\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), changed, 0o644); err != nil {
		t.Fatal(err)
	}
	object := gitTestHash(changed)
	if err := storeObject(root, object, changed); err != nil {
		t.Fatal(err)
	}
	commit := localCommit{
		ID: "legacy-local", Parent: "remote-base", Message: "change readme", CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		Changes: []commitChange{{Kind: "modified", Path: "README.md", Object: object, Mode: 0o644}},
	}
	if err := saveLocalCommit(root, commit); err != nil {
		t.Fatal(err)
	}
	state := workspaceState{
		Version: 3, Provider: ForgeGitea,
		Remote: RepositoryRef{Forge: ForgeGitea, Server: "https://example.test", Namespace: "team", Name: "repo"},
		Branch: "main", BaseCommit: "remote-base", Queue: []string{commit.ID}, History: []string{commit.ID}, LocalHead: commit.ID,
		Files: map[string]fileState{"README.md": {Hash: object, Mode: 0o644}},
	}
	state.syncLegacyIdentity()
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := atomicWrite(filepath.Join(root, ".gew", "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, state, migrationForge{head: "remote-base", content: map[string][]byte{"README.md": base}}
}

func TestGitMigrationDryRunWritesNothing(t *testing.T) {
	root, state, remote := makeMigrationWorkspace(t)
	before, err := directoryDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := migrateToGit(context.Background(), root, &state, remote, true, "Gew User", "gew@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	after, err := directoryDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || manifest.Phase != "" || len(manifest.Queue) != 1 {
		t.Fatalf("dry run mutated workspace or manifest = %#v", manifest)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created .git: %v", err)
	}
}

func TestGitMigrationReplaysQueueAndRetainsLegacy(t *testing.T) {
	root, state, remote := makeMigrationWorkspace(t)
	manifest, err := migrateToGit(context.Background(), root, &state, remote, false, "Gew User", "gew@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if state.Backend != WorkspaceGit || state.Version != stateVersion || state.Hybrid == nil || manifest.Phase != "complete" {
		t.Fatalf("state/manifest = %#v / %#v", state, manifest)
	}
	if manifest.CommitMap["legacy-local"] == "" {
		t.Fatalf("commit map = %#v", manifest.CommitMap)
	}
	if _, err := os.Stat(filepath.Join(root, ".gew", "legacy", "v3-"+manifest.ID, "state.json")); err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if err != nil || len(pending) != 1 || pending[0].Message != "change readme" {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(content) != "changed\n" {
		t.Fatalf("worktree = %q", content)
	}
}

func TestGitMigrationAcceptsReleasedVersionTwoState(t *testing.T) {
	root, state, remote := makeMigrationWorkspace(t)
	state.Version = 2
	state.Backend = ""
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(root, ".gew", "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := migrateToGit(context.Background(), root, &state, remote, false, "Gew User", "gew@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceVersion != 2 {
		t.Fatalf("source version = %d", manifest.SourceVersion)
	}
	if _, err := os.Stat(filepath.Join(root, ".gew", "legacy", "v2-"+manifest.ID, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestGitMigrationRefusesExistingGitAndStaleRemote(t *testing.T) {
	root, state, remote := makeMigrationWorkspace(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateToGit(context.Background(), root, &state, remote, true, "Gew User", "gew@example.invalid"); err == nil || !strings.Contains(err.Error(), "existing .git") {
		t.Fatalf("existing .git error = %v", err)
	}
	os.Remove(filepath.Join(root, ".git"))
	remote.head = "advanced"
	if _, err := migrateToGit(context.Background(), root, &state, remote, true, "Gew User", "gew@example.invalid"); err == nil || !strings.Contains(err.Error(), "advanced") {
		t.Fatalf("stale remote error = %v", err)
	}
}

func directoryDigest(root string) (string, error) {
	metadata, err := scanWorkspace(root)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, ".gew", "state.json"))
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(metadata)
	return gitTestHash(append(encoded, data...)), nil
}

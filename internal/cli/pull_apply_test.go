package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gew/internal/workspace"
)

func TestPullJournalRecoveryRestoresOldBytesAndState(t *testing.T) {
	root := t.TempDir()
	oldState := workspaceState{Version: stateVersion, Provider: ForgeGitea, Remote: RepositoryRef{Forge: ForgeGitea, Server: "https://example.test", Namespace: "acme", Name: "demo"}, Branch: "main", BaseCommit: "old", Files: map[string]fileState{"file.txt": {Hash: "old", Mode: 0o644, Size: 4}}}
	newState := oldState
	newState.BaseCommit = "new"
	if err := saveState(root, newState); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "pull-test"
	backup := filepath.Join(root, ".gew", "recovery", id, "file.txt")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := pullJournal{Version: pullJournalVersion, ID: id, TargetCommit: "new", OldState: oldState, Operations: []workspace.PullOperation{{Kind: workspace.PullModify, Path: "file.txt"}}}
	data, _ := json.Marshal(journal)
	if err := os.WriteFile(pullJournalPath(root), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverPull(root); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(content) != "old\n" {
		t.Fatalf("restored content = %q, %v", content, err)
	}
	stateData, err := os.ReadFile(filepath.Join(root, ".gew", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state workspaceState
	if err := json.Unmarshal(stateData, &state); err != nil || state.BaseCommit != "old" {
		t.Fatalf("restored state = %#v, %v", state, err)
	}
	if _, err := os.Stat(pullJournalPath(root)); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
}

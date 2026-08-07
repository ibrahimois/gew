package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type gitMigrationManifest struct {
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	SourceVersion int               `json:"source_version"`
	StateDigest   string            `json:"state_digest"`
	Queue         []string          `json:"queue"`
	RemoteHead    string            `json:"remote_head"`
	Destination   string            `json:"destination"`
	Phase         string            `json:"phase"`
	CommitMap     map[string]string `json:"commit_map,omitempty"`
}

func (a app) migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	target := flags.String("to", "", "destination backend")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	authorName := flags.String("author-name", "", "local Git author name")
	authorEmail := flags.String("author-email", "", "local Git author email")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *target != string(WorkspaceGit) {
		return errors.New("usage: gew migrate --to git [--dry-run] [--author-name NAME --author-email EMAIL]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	remote, err := forgeForWorkspace(state)
	if err != nil {
		return err
	}
	manifest, err := migrateToGit(context.Background(), root, &state, remote, *dryRun, *authorName, *authorEmail)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprintf(a.out, "Migration dry-run passed: %d queued commit(s), remote %.12s, no files written.\n", len(manifest.Queue), manifest.RemoteHead)
		return nil
	}
	fmt.Fprintf(a.out, "Migrated workspace to the hybrid Git backend (%s); legacy data retained under .gew/legacy/v%d-%s.\n", manifest.ID, manifest.SourceVersion, manifest.ID)
	return nil
}

func migrateToGit(ctx context.Context, root string, state *workspaceState, remote Forge, dryRun bool, authorName, authorEmail string) (gitMigrationManifest, error) {
	manifest := gitMigrationManifest{Version: 1, SourceVersion: state.Version, Destination: string(WorkspaceGit), Queue: append([]string(nil), state.Queue...), CommitMap: make(map[string]string)}
	backend, err := normalizeWorkspaceBackend(state.Backend)
	if err != nil {
		return manifest, err
	}
	if backend != WorkspaceGew || state.Version < 1 || state.Version > 3 {
		return manifest, errors.New("migration requires a version-1, version-2, or version-3 gew backend workspace")
	}
	state.Backend = backend
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		return manifest, errors.New("refusing to overwrite or adopt an existing .git path")
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	if mergeState, err := loadMergeState(root); err != nil {
		return manifest, err
	} else if mergeState != nil {
		return manifest, errors.New("finish or abort the active merge before migration")
	}
	index, err := loadIndex(root)
	if err != nil {
		return manifest, err
	}
	if len(index.Entries) != 0 {
		return manifest, errors.New("migration requires an empty staging index")
	}
	changes, err := workspaceChanges(root, *state)
	if err != nil {
		return manifest, err
	}
	if len(changes) != 0 {
		return manifest, errors.New("migration requires a clean worktree")
	}
	name := strings.TrimSpace(authorName)
	email := strings.TrimSpace(authorEmail)
	if name == "" {
		name = strings.TrimSpace(os.Getenv("GEW_AUTHOR_NAME"))
	}
	if email == "" {
		email = strings.TrimSpace(os.Getenv("GEW_AUTHOR_EMAIL"))
	}
	if name == "" || email == "" || strings.ContainsAny(name+email, "\x00\r\n") {
		return manifest, errors.New("migration requires --author-name/--author-email or GEW_AUTHOR_NAME/GEW_AUTHOR_EMAIL")
	}
	commits := make([]localCommit, 0, len(state.Queue))
	for _, commitID := range state.Queue {
		commit, err := loadLocalCommit(root, commitID)
		if err != nil {
			return manifest, fmt.Errorf("validate queued commit %s: %w", commitID, err)
		}
		for _, change := range commit.Changes {
			if change.Kind == "deleted" {
				continue
			}
			content, err := os.ReadFile(objectPath(root, change.Object))
			if err != nil {
				return manifest, fmt.Errorf("validate object %s: %w", change.Object, err)
			}
			sum := sha256.Sum256(content)
			if hex.EncodeToString(sum[:]) != change.Object {
				return manifest, fmt.Errorf("object %s failed its content hash", change.Object)
			}
		}
		commits = append(commits, commit)
	}
	remoteHead, err := remote.Head(ctx, state.Remote, state.Branch)
	if err != nil {
		if isRemoteNotFound(err) && state.BaseCommit == "" {
			remoteHead = ""
		} else {
			return manifest, err
		}
	}
	if remoteHead != state.BaseCommit {
		return manifest, fmt.Errorf("remote branch advanced from %.12s to %.12s; pull before migration", state.BaseCommit, remoteHead)
	}
	manifest.RemoteHead = remoteHead
	stateBytes, err := os.ReadFile(filepath.Join(root, ".gew", "state.json"))
	if err != nil {
		return manifest, err
	}
	stateSum := sha256.Sum256(stateBytes)
	manifest.StateDigest = hex.EncodeToString(stateSum[:])
	idSum := sha256.Sum256([]byte(manifest.StateDigest + "\x00" + strings.Join(state.Queue, "\x00")))
	manifest.ID = hex.EncodeToString(idSum[:8])
	baseFiles := make(map[string][]byte)
	if remoteHead != "" {
		baseFiles, _, err = remoteByteSnapshot(ctx, remote, state.Remote, remoteHead)
		if err != nil {
			return manifest, err
		}
	}
	if dryRun {
		return manifest, nil
	}
	manifest.Phase = "prepared"
	if err := saveMigrationManifest(filepath.Join(root, ".gew", "migration.json"), manifest); err != nil {
		return manifest, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(root), ".gew-migrate-")
	if err != nil {
		return manifest, err
	}
	defer os.RemoveAll(temporary)
	if err := writeByteSnapshot(temporary, baseFiles); err != nil {
		return manifest, err
	}
	temporaryState := *state
	temporaryState.Files = make(map[string]fileState)
	temporaryState.Queue = nil
	temporaryState.History = nil
	temporaryState.LocalHead = ""
	if err := initializeGitWorkspace(temporary, &temporaryState, remoteHead == ""); err != nil {
		return manifest, err
	}
	anchorOID := temporaryState.Hybrid.LastLocalOID
	_, worktree, err := openGitWorkspace(temporary, temporaryState)
	if err != nil {
		return manifest, err
	}
	for _, commit := range commits {
		for _, changed := range commit.Changes {
			destination := filepath.Join(temporary, filepath.FromSlash(changed.Path))
			if changed.Kind == "deleted" {
				if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
					return manifest, err
				}
				continue
			}
			content, err := os.ReadFile(objectPath(root, changed.Object))
			if err != nil {
				return manifest, err
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return manifest, err
			}
			if err := os.WriteFile(destination, content, 0o644); err != nil {
				return manifest, err
			}
		}
		if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return manifest, err
		}
		when := commit.CreatedAt
		if when.IsZero() {
			when = time.Now().UTC()
		}
		signature := &object.Signature{Name: name, Email: email, When: when}
		oid, err := worktree.Commit(commit.Message, &git.CommitOptions{Author: signature, Committer: signature})
		if err != nil {
			return manifest, err
		}
		manifest.CommitMap[commit.ID] = oid.String()
	}
	tip, err := gitWorktreeSnapshot(temporary)
	if err != nil {
		return manifest, err
	}
	current, err := gitWorktreeSnapshot(root)
	if err != nil {
		return manifest, err
	}
	if !equalByteSnapshots(tip, current) {
		return manifest, errors.New("migrated Git tip does not match the original worktree")
	}
	manifest.Phase = "installing"
	if err := saveMigrationManifest(filepath.Join(root, ".gew", "migration.json"), manifest); err != nil {
		return manifest, err
	}
	if err := os.Rename(filepath.Join(temporary, ".git"), filepath.Join(root, ".git")); err != nil {
		return manifest, err
	}
	legacyRoot := filepath.Join(root, ".gew", "legacy", fmt.Sprintf("v%d-%s", manifest.SourceVersion, manifest.ID))
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		return manifest, err
	}
	for _, name := range []string{"state.json", "index.json", "objects", "commits"} {
		source := filepath.Join(root, ".gew", name)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyMigrationPath(source, filepath.Join(legacyRoot, name)); err != nil {
			return manifest, err
		}
	}
	manifest.Phase = "installed"
	if err := saveMigrationManifest(filepath.Join(legacyRoot, "manifest.json"), manifest); err != nil {
		return manifest, err
	}
	state.Backend = WorkspaceGit
	state.Hybrid = temporaryState.Hybrid
	state.Hybrid.MigrationID = manifest.ID
	state.Version = stateVersion
	if anchorOID != "" {
		if err := saveGitExportReceipt(root, gitExportReceipt{Version: exportReceiptVersion, LocalOID: anchorOID, ProviderID: remoteHead, ProviderBase: remoteHead, Message: "migrated remote anchor"}); err != nil {
			return manifest, err
		}
	}
	if err := saveState(root, *state); err != nil {
		return manifest, err
	}
	manifest.Phase = "complete"
	if err := saveMigrationManifest(filepath.Join(root, ".gew", "migration.json"), manifest); err != nil {
		return manifest, err
	}
	reopened, _, err := openGitWorkspace(root, *state)
	if err != nil {
		return manifest, err
	}
	pending, err := pendingGitCommits(reopened, state.Hybrid.TrackingRef)
	if err != nil || len(pending) != len(state.Queue) {
		return manifest, fmt.Errorf("post-migration pending commit validation failed: got %d, want %d: %w", len(pending), len(state.Queue), err)
	}
	return manifest, nil
}

func writeByteSnapshot(root string, files map[string][]byte) error {
	for filePath, content := range files {
		cleaned, err := validateRemotePath(filePath)
		if err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(cleaned))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func equalByteSnapshots(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for filePath, content := range left {
		if other, ok := right[filePath]; !ok || !strings.EqualFold(hex.EncodeToString(content), hex.EncodeToString(other)) {
			return false
		}
	}
	return true
}

func saveMigrationManifest(destination string, manifest gitMigrationManifest) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(destination, append(data, '\n'), 0o600)
}

func copyMigrationPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("migration archive refuses symlink %s", source)
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyMigrationPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(0o600))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

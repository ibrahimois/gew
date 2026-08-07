package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const exportReceiptVersion = 1

func initializeGitWorkspace(root string, state *workspaceState, empty bool) error {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		return errors.New("refusing to overwrite or adopt an existing .git path")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	repository, err := git.PlainInit(root, false)
	if err != nil {
		return err
	}
	branchName := plumbing.NewBranchReferenceName(strings.TrimPrefix(state.Branch, "refs/heads/"))
	if err := branchName.Validate(); err != nil {
		return fmt.Errorf("invalid local Git branch %q: %w", state.Branch, err)
	}
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branchName)); err != nil {
		return err
	}
	trackingName, err := gewTrackingRef(state.Provider, state.Branch)
	if err != nil {
		return err
	}
	state.Backend = WorkspaceGit
	state.Hybrid = &hybridState{BranchRef: branchName.String(), TrackingRef: trackingName, LastProviderID: state.BaseCommit}
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(root, ".git", "info", "exclude"), []byte("/.gew/\n"), 0o644); err != nil {
		return err
	}
	if empty {
		return nil
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return err
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	signature := snapshotSignature()
	message := fmt.Sprintf("gew snapshot %s %.12s", state.Provider, state.BaseCommit)
	oid, err := worktree.Commit(message, &git.CommitOptions{Author: signature, Committer: signature})
	if err != nil {
		return err
	}
	trackingRef := plumbing.NewHashReference(plumbing.ReferenceName(trackingName), oid)
	if err := repository.Storer.SetReference(trackingRef); err != nil {
		return err
	}
	state.Hybrid.LastLocalOID = oid.String()
	return nil
}

func snapshotSignature() *object.Signature {
	return &object.Signature{Name: "Gew REST snapshot", Email: "snapshot@gew.invalid", When: time.Unix(0, 0).UTC()}
}

func openGitWorkspace(root string, state workspaceState) (*git.Repository, *git.Worktree, error) {
	if state.Backend != WorkspaceGit || state.Hybrid == nil {
		return nil, nil, errors.New("workspace is not using the git backend")
	}
	repository, err := git.PlainOpen(root)
	if err != nil {
		return nil, nil, err
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return nil, nil, err
	}
	return repository, worktree, nil
}

func validateHybridState(root string, state *workspaceState) error {
	if state.Hybrid == nil || state.Hybrid.BranchRef == "" || state.Hybrid.TrackingRef == "" {
		return errors.New("hybrid workspace state is incomplete")
	}
	expectedTracking, err := gewTrackingRef(state.Provider, state.Branch)
	if err != nil || expectedTracking != state.Hybrid.TrackingRef {
		return errors.New("hybrid workspace tracking ref does not match provider and branch")
	}
	repository, err := git.PlainOpen(root)
	if err != nil {
		return fmt.Errorf("open hybrid .git repository: %w", err)
	}
	head, err := repository.Head()
	if err == nil && head.Name().String() != state.Hybrid.BranchRef {
		return fmt.Errorf("hybrid HEAD points to %s, expected %s", head.Name(), state.Hybrid.BranchRef)
	}
	if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return err
	}
	tracking, err := repository.Reference(plumbing.ReferenceName(state.Hybrid.TrackingRef), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) && state.BaseCommit == "" && state.Hybrid.LastLocalOID == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read hybrid tracking ref: %w", err)
	}
	trackingOID := tracking.Hash().String()
	if trackingOID != state.Hybrid.LastLocalOID {
		receipt, err := loadGitExportReceipt(root, trackingOID)
		if err != nil {
			return fmt.Errorf("hybrid tracking/state mismatch has no valid recovery receipt: %w", err)
		}
		state.Hybrid.LastLocalOID = trackingOID
		state.Hybrid.LastProviderID = receipt.ProviderID
		state.BaseCommit = receipt.ProviderID
	}
	receipt, err := loadGitExportReceipt(root, state.Hybrid.LastLocalOID)
	if err != nil {
		return fmt.Errorf("hybrid tracking receipt is invalid: %w", err)
	}
	if receipt.ProviderID != state.BaseCommit || state.Hybrid.LastProviderID != state.BaseCommit {
		return errors.New("hybrid receipt/provider head mismatch requires export reconciliation")
	}
	return nil
}

func loadGitExportReceipt(root, oid string) (gitExportReceipt, error) {
	data, err := os.ReadFile(receiptPath(root, oid))
	if err != nil {
		return gitExportReceipt{}, err
	}
	var receipt gitExportReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return gitExportReceipt{}, err
	}
	if receipt.Version != exportReceiptVersion || receipt.LocalOID != oid || receipt.ProviderID == "" {
		return gitExportReceipt{}, errors.New("invalid Git export receipt")
	}
	return receipt, nil
}

func gitAuthor(repository *git.Repository, explicitName, explicitEmail string) (*object.Signature, error) {
	name := strings.TrimSpace(explicitName)
	email := strings.TrimSpace(explicitEmail)
	if name == "" {
		name = strings.TrimSpace(os.Getenv("GEW_AUTHOR_NAME"))
	}
	if email == "" {
		email = strings.TrimSpace(os.Getenv("GEW_AUTHOR_EMAIL"))
	}
	if name == "" || email == "" {
		configuration, err := repository.ConfigScoped(gitconfig.LocalScope)
		if err == nil {
			if name == "" {
				name = strings.TrimSpace(configuration.User.Name)
			}
			if email == "" {
				email = strings.TrimSpace(configuration.User.Email)
			}
		}
	}
	if name == "" || email == "" || strings.ContainsAny(name+email, "\x00\r\n") {
		return nil, errors.New("Git commit identity is required; set GEW_AUTHOR_NAME and GEW_AUTHOR_EMAIL or local user.name/user.email")
	}
	return &object.Signature{Name: name, Email: email, When: time.Now().UTC()}, nil
}

func gitSnapshots(repository *git.Repository, worktree *git.Worktree) (map[string][]byte, map[string][]byte, map[string][]byte, error) {
	head := make(map[string][]byte)
	reference, err := repository.Head()
	if err == nil {
		head, err = gitCommitSnapshot(repository, reference.Hash())
	}
	if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil, nil, err
	}
	index, err := gitIndexSnapshot(repository)
	if err != nil {
		return nil, nil, nil, err
	}
	worktreeFiles, err := gitWorktreeSnapshot(worktree.Filesystem.Root())
	if err != nil {
		return nil, nil, nil, err
	}
	return head, index, worktreeFiles, nil
}

func gitCommitSnapshot(repository *git.Repository, oid plumbing.Hash) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if oid.IsZero() {
		return result, nil
	}
	commit, err := repository.CommitObject(oid)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	files := tree.Files()
	err = files.ForEach(func(file *object.File) error {
		cleaned, err := validateRemotePath(file.Name)
		if err != nil {
			return err
		}
		reader, err := file.Reader()
		if err != nil {
			return err
		}
		data, readErr := readBounded(reader, maxRemoteSnapshot)
		closeErr := reader.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		result[cleaned] = data
		return nil
	})
	return result, err
}

func gitIndexSnapshot(repository *git.Repository) (map[string][]byte, error) {
	result := make(map[string][]byte)
	index, err := repository.Storer.Index()
	if err != nil {
		return nil, err
	}
	for _, entry := range index.Entries {
		if entry.Stage != 0 {
			continue
		}
		cleaned, err := validateRemotePath(entry.Name)
		if err != nil {
			return nil, err
		}
		blob, err := repository.BlobObject(entry.Hash)
		if err != nil {
			return nil, err
		}
		reader, err := blob.Reader()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result[cleaned] = data
	}
	return result, nil
}

func gitWorktreeSnapshot(root string) (map[string][]byte, error) {
	metadata, err := scanWorkspace(root)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(metadata))
	for filePath := range metadata {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(filePath)))
		if err != nil {
			return nil, err
		}
		result[filePath] = content
	}
	return result, nil
}

func byteSnapshotChanges(before, after map[string][]byte) []change {
	paths := make(map[string]bool, len(before)+len(after))
	for filePath := range before {
		paths[filePath] = true
	}
	for filePath := range after {
		paths[filePath] = true
	}
	result := make([]change, 0)
	for filePath := range paths {
		left, leftOK := before[filePath]
		right, rightOK := after[filePath]
		switch {
		case !leftOK && rightOK:
			result = append(result, change{Kind: "created", Path: filePath})
		case leftOK && !rightOK:
			result = append(result, change{Kind: "deleted", Path: filePath})
		case leftOK && rightOK && !bytes.Equal(left, right):
			result = append(result, change{Kind: "modified", Path: filePath})
		}
	}
	return sortedChanges(result)
}

func (a app) gitAdd(root string, state workspaceState, arguments []string, all bool) error {
	_, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	before, err := worktree.Status()
	if err != nil {
		return err
	}
	if all {
		if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return err
		}
	} else {
		selectors, err := selectorsForArgs(root, arguments, false)
		if err != nil {
			return err
		}
		for _, selector := range selectors {
			if selector.path == "" {
				continue
			}
			if _, err := worktree.Add(selector.path); err != nil {
				return fmt.Errorf("stage %s: %w", selector.path, err)
			}
		}
	}
	after, err := worktree.Status()
	if err != nil {
		return err
	}
	staged := 0
	for filePath, fileStatus := range after {
		if fileStatus.Staging != git.Unmodified {
			if previous, ok := before[filePath]; !ok || previous.Staging != fileStatus.Staging {
				staged++
			} else {
				staged++
			}
		}
	}
	if staged == 0 {
		fmt.Fprintln(a.out, "No changes matched.")
	} else {
		fmt.Fprintf(a.out, "Staged %d file change(s).\n", staged)
	}
	return nil
}

func (a app) gitReset(root string, state workspaceState, arguments []string) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	status, err := worktree.Status()
	if err != nil {
		return err
	}
	staged := 0
	for _, fileStatus := range status {
		if fileStatus.Staging != git.Unmodified {
			staged++
		}
	}
	if staged == 0 {
		fmt.Fprintln(a.out, "Nothing is staged.")
		return nil
	}
	if len(arguments) == 0 {
		head, headErr := repository.Head()
		if errors.Is(headErr, plumbing.ErrReferenceNotFound) {
			index, err := repository.Storer.Index()
			if err != nil {
				return err
			}
			index.Entries = nil
			if err := repository.Storer.SetIndex(index); err != nil {
				return err
			}
		} else if headErr != nil {
			return headErr
		} else if err := worktree.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.MixedReset}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Unstaged %d file change(s).\n", staged)
		return nil
	}
	selectors, err := selectorsForArgs(root, arguments, false)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		files = append(files, selector.path)
	}
	if err := worktree.Restore(&git.RestoreOptions{Staged: true, Files: files}); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Unstaged %d path(s).\n", len(files))
	return nil
}

func (a app) gitCommit(root string, state workspaceState, message, authorName, authorEmail string) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	status, err := worktree.Status()
	if err != nil {
		return err
	}
	staged := 0
	for _, fileStatus := range status {
		if fileStatus.Staging != git.Unmodified {
			staged++
		}
	}
	if staged == 0 {
		return errors.New("nothing staged; use 'gew add' first")
	}
	author, err := gitAuthor(repository, authorName, authorEmail)
	if err != nil {
		return err
	}
	oid, err := worktree.Commit(message, &git.CommitOptions{Author: author, Committer: author})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "[%s %.12s] %s\n", state.Branch, oid, message)
	fmt.Fprintf(a.out, " %d file change(s) committed locally.\n", staged)
	return nil
}

func (a app) gitLog(root string, state workspaceState, oneline bool) error {
	repository, _, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	head, err := repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		fmt.Fprintln(a.out, "No local Git commits yet.")
		return nil
	}
	if err != nil {
		return err
	}
	iterator, err := repository.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return err
	}
	defer iterator.Close()
	return iterator.ForEach(func(commit *object.Commit) error {
		label := "unpushed"
		var receipt gitExportReceipt
		data, readErr := os.ReadFile(receiptPath(root, commit.Hash.String()))
		if readErr == nil && json.Unmarshal(data, &receipt) == nil {
			label = "exported " + shortID(receipt.ProviderID)
		}
		if oneline {
			fmt.Fprintf(a.out, "%.12s %-20s %s\n", commit.Hash, "["+label+"]", firstLine(commit.Message))
			return nil
		}
		fmt.Fprintf(a.out, "commit %s (%s)\nDate:   %s\n\n    %s\n\n", commit.Hash, label, commit.Author.When.Format(time.RFC3339), strings.ReplaceAll(commit.Message, "\n", "\n    "))
		return nil
	})
}

func (a app) gitStatus(root string, state workspaceState, asJSON bool) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	headFiles, indexFiles, worktreeFiles, err := gitSnapshots(repository, worktree)
	if err != nil {
		return err
	}
	staged := byteSnapshotChanges(headFiles, indexFiles)
	unstaged := byteSnapshotChanges(indexFiles, worktreeFiles)
	pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if err != nil {
		return err
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if asJSON {
		payload := struct {
			Repository string          `json:"repository"`
			Branch     string          `json:"branch"`
			BaseCommit string          `json:"base_commit"`
			Backend    string          `json:"backend"`
			Unpushed   int             `json:"unpushed_commits"`
			Staged     []change        `json:"staged"`
			Unstaged   []change        `json:"unstaged"`
			Merging    bool            `json:"merging"`
			Conflicts  []mergeConflict `json:"conflicts,omitempty"`
		}{state.Remote.DisplayName(), state.Branch, state.BaseCommit, string(state.Backend), len(pending), staged, unstaged, mergeState != nil, nil}
		if mergeState != nil {
			payload.Conflicts = mergeState.Conflicts
		}
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	fmt.Fprintf(a.out, "On branch %s\nRepository: %s (%s)\nBackend: git hybrid\n", state.Branch, state.Remote.DisplayName(), state.Provider)
	if len(pending) > 0 {
		fmt.Fprintf(a.out, "Your branch has %d unpushed local Git commit(s).\n", len(pending))
	}
	if len(staged) == 0 && len(unstaged) == 0 && len(pending) == 0 && mergeState == nil {
		fmt.Fprintln(a.out, "Workspace is clean.")
		return nil
	}
	if len(staged) > 0 {
		fmt.Fprintln(a.out, "\nChanges to be committed:")
		for _, item := range staged {
			fmt.Fprintf(a.out, "  %-8s %s\n", item.Kind, item.Path)
		}
	}
	if len(unstaged) > 0 {
		fmt.Fprintln(a.out, "\nChanges not staged for commit:")
		for _, item := range unstaged {
			fmt.Fprintf(a.out, "  %-8s %s\n", item.Kind, item.Path)
		}
	}
	return nil
}

func (a app) gitDiff(root string, state workspaceState, staged bool) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	headFiles, indexFiles, worktreeFiles, err := gitSnapshots(repository, worktree)
	if err != nil {
		return err
	}
	before, after := indexFiles, worktreeFiles
	if staged {
		before, after = headFiles, indexFiles
	}
	for _, item := range byteSnapshotChanges(before, after) {
		beforeContent, beforeOK := before[item.Path]
		afterContent, afterOK := after[item.Path]
		printUnifiedDiff(a.out, item.Path, beforeContent, beforeOK, afterContent, afterOK)
	}
	return nil
}

func remoteByteSnapshot(ctx context.Context, remote Forge, ref RepositoryRef, commit string) (map[string][]byte, map[string]RemoteFile, error) {
	files, err := remote.Tree(ctx, ref, commit)
	if err != nil {
		return nil, nil, err
	}
	archive, err := remote.Snapshot(ctx, ref, commit)
	if err != nil {
		return nil, nil, err
	}
	directory, err := os.MkdirTemp("", "gew-remote-snapshot-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(directory)
	if err := extractArchive(archive, directory); err != nil {
		return nil, nil, err
	}
	result, err := gitWorktreeSnapshot(directory)
	if err != nil {
		return nil, nil, err
	}
	if len(result) != len(files) {
		return nil, nil, fmt.Errorf("remote snapshot/tree entry mismatch at %.12s", commit)
	}
	return result, files, nil
}

func replaceGitWorktree(root string, files map[string][]byte) error {
	current, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	for filePath := range current {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(filePath))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return writeByteSnapshot(root, files)
}

func materializeSnapshot(files map[string][]byte) (string, error) {
	directory, err := os.MkdirTemp("", "gew-git-snapshot-")
	if err != nil {
		return "", err
	}
	if err := writeByteSnapshot(directory, files); err != nil {
		os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}

func (a app) gitPull(root string, state workspaceState, remote Forge, ffOnly bool) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	if mergeState, err := loadMergeState(root); err != nil {
		return err
	} else if mergeState != nil {
		return errors.New("a merge is already in progress; continue or abort it first")
	}
	headFiles, indexFiles, worktreeFiles, err := gitSnapshots(repository, worktree)
	if err != nil {
		return err
	}
	if len(byteSnapshotChanges(headFiles, indexFiles)) != 0 {
		return errors.New("workspace has staged changes; commit or reset them before pulling")
	}
	if len(byteSnapshotChanges(indexFiles, worktreeFiles)) != 0 {
		return errors.New("hybrid pull requires a clean worktree; commit or restore unstaged changes first")
	}
	pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if err != nil {
		return err
	}
	remoteHead, err := remote.Head(context.Background(), state.Remote, state.Branch)
	if err != nil {
		if isRemoteNotFound(err) && state.BaseCommit == "" {
			fmt.Fprintln(a.out, "Already up to date (remote repository is empty).")
			return nil
		}
		return err
	}
	if remoteHead == state.BaseCommit {
		fmt.Fprintln(a.out, "Already up to date.")
		return nil
	}
	if ffOnly && len(pending) != 0 {
		return errors.New("fast-forward pull is not possible with unpushed local Git commits")
	}
	theirs, remoteFiles, err := remoteByteSnapshot(context.Background(), remote, state.Remote, remoteHead)
	if err != nil {
		return err
	}
	oldHead, err := repository.Head()
	if err != nil {
		return err
	}
	tracking := plumbing.NewHash(state.Hybrid.LastLocalOID)
	if len(pending) == 0 {
		if err := replaceGitWorktree(root, theirs); err != nil {
			return err
		}
		if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return err
		}
		signature := snapshotSignature()
		oid, err := worktree.Commit(fmt.Sprintf("gew snapshot %s %.12s", state.Provider, remoteHead), &git.CommitOptions{Author: signature, Committer: signature, AllowEmptyCommits: true})
		if err != nil {
			return err
		}
		state.BaseCommit = remoteHead
		state.Hybrid.LastLocalOID = oid.String()
		state.Hybrid.LastProviderID = remoteHead
		state.Files = mergeFileMetadataFromBytes(theirs, remoteFiles)
		if err := saveGitExportReceipt(root, gitExportReceipt{Version: exportReceiptVersion, LocalOID: oid.String(), ProviderID: remoteHead, ProviderBase: remoteHead, Message: "imported remote snapshot"}); err != nil {
			return err
		}
		if err := advanceGitTracking(repository, state.Hybrid.TrackingRef, tracking, oid); err != nil {
			return err
		}
		if err := saveState(root, state); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Updated %s to %.12s.\n", state.Branch, remoteHead)
		return nil
	}
	baseFiles, err := gitCommitSnapshot(repository, tracking)
	if err != nil {
		return err
	}
	ours := headFiles
	baseDirectory, err := materializeSnapshot(baseFiles)
	if err != nil {
		return err
	}
	defer os.RemoveAll(baseDirectory)
	oursDirectory, err := materializeSnapshot(ours)
	if err != nil {
		return err
	}
	defer os.RemoveAll(oursDirectory)
	theirsDirectory, err := materializeSnapshot(theirs)
	if err != nil {
		return err
	}
	defer os.RemoveAll(theirsDirectory)
	resultDirectory, err := os.MkdirTemp("", "gew-git-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(resultDirectory)
	conflicts, err := mergeDirectories(oursDirectory, baseDirectory, theirsDirectory, resultDirectory)
	if err != nil {
		return err
	}
	supersededName := plumbing.ReferenceName(fmt.Sprintf("refs/gew/superseded/%d", time.Now().UTC().UnixNano()))
	if err := repository.Storer.SetReference(plumbing.NewHashReference(supersededName, oldHead.Hash())); err != nil {
		return err
	}
	branchRef := plumbing.ReferenceName(state.Hybrid.BranchRef)
	if err := repository.Storer.SetReference(plumbing.NewHashReference(branchRef, tracking)); err != nil {
		return err
	}
	if err := replaceGitWorktree(root, theirs); err != nil {
		return err
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	signature := snapshotSignature()
	anchor, err := worktree.Commit(fmt.Sprintf("gew snapshot %s %.12s", state.Provider, remoteHead), &git.CommitOptions{Author: signature, Committer: signature, AllowEmptyCommits: true})
	if err != nil {
		return err
	}
	previousState := state
	previousHybrid := *state.Hybrid
	previousState.Hybrid = &previousHybrid
	state.BaseCommit = remoteHead
	state.Hybrid.LastLocalOID = anchor.String()
	state.Hybrid.LastProviderID = remoteHead
	state.Files = mergeFileMetadataFromBytes(theirs, remoteFiles)
	if err := saveGitExportReceipt(root, gitExportReceipt{Version: exportReceiptVersion, LocalOID: anchor.String(), ProviderID: remoteHead, ProviderBase: remoteHead, Message: "imported remote snapshot"}); err != nil {
		return err
	}
	if err := advanceGitTracking(repository, state.Hybrid.TrackingRef, tracking, anchor); err != nil {
		return err
	}
	mergedFiles, err := gitWorktreeSnapshot(resultDirectory)
	if err != nil {
		return err
	}
	if err := replaceGitWorktree(root, mergedFiles); err != nil {
		return err
	}
	mergeState := activeMerge{
		Version: mergeStateVersion, RemoteCommit: remoteHead, Message: fmt.Sprintf("Merge remote %s into local changes", state.Branch),
		PreviousState: previousState, Conflicts: conflicts, GitPreviousHead: oldHead.Hash().String(),
		GitRemoteAnchor: anchor.String(), GitTrackingBefore: tracking.String(),
	}
	if len(conflicts) > 0 {
		if err := saveMergeState(root, mergeState); err != nil {
			return err
		}
		if err := writeGitConflictSides(root, baseFiles, ours, theirs, conflicts); err != nil {
			return err
		}
		if err := saveState(root, state); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Merge stopped with %d conflict(s). Resolve them, then run 'gew merge --continue'.\n", len(conflicts))
		return errors.New("automatic merge failed; conflicts require resolution")
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	mergeSignature := &object.Signature{Name: "Gew REST merge", Email: "merge@gew.invalid", When: time.Now().UTC()}
	mergeOID, err := worktree.Commit(mergeState.Message, &git.CommitOptions{Author: mergeSignature, Committer: mergeSignature, AllowEmptyCommits: true})
	if err != nil {
		return err
	}
	if err := saveState(root, state); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Merged remote %s and replaced %d pending commit(s) with local Git commit %.12s.\n", state.Branch, len(pending), mergeOID)
	return nil
}

func writeGitConflictSides(root string, base, ours, theirs map[string][]byte, conflicts []mergeConflict) error {
	directory := filepath.Join(root, ".gew", "conflicts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	for _, conflict := range conflicts {
		if !conflict.Binary {
			continue
		}
		name := strings.ReplaceAll(conflict.Path, "/", "__")
		for suffix, snapshot := range map[string]map[string][]byte{"base": base, "ours": ours, "theirs": theirs} {
			content, exists := snapshot[conflict.Path]
			if !exists {
				content = nil
			}
			if err := atomicWrite(filepath.Join(directory, name+"."+suffix), content, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeFileMetadataFromBytes(files map[string][]byte, remote map[string]RemoteFile) map[string]fileState {
	result := make(map[string]fileState, len(files))
	for filePath, content := range files {
		sum := sha256.Sum256(content)
		metadata := fileState{Hash: hex.EncodeToString(sum[:]), Mode: 0o644}
		if item, ok := remote[filePath]; ok {
			metadata.BlobSHA = item.BlobID
			if item.Mode != 0 {
				metadata.Mode = item.Mode
			}
		}
		result[filePath] = metadata
	}
	return result
}

func (a app) gitMerge(root string, state workspaceState, abort, continueMerge bool, message string) error {
	repository, worktree, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState == nil || mergeState.GitPreviousHead == "" {
		return errors.New("no hybrid Git merge is in progress")
	}
	branchRef := plumbing.ReferenceName(state.Hybrid.BranchRef)
	if abort {
		previous := plumbing.NewHash(mergeState.GitPreviousHead)
		if err := repository.Storer.SetReference(plumbing.NewHashReference(branchRef, previous)); err != nil {
			return err
		}
		trackingName := plumbing.ReferenceName(state.Hybrid.TrackingRef)
		if err := repository.Storer.SetReference(plumbing.NewHashReference(trackingName, plumbing.NewHash(mergeState.GitTrackingBefore))); err != nil {
			return err
		}
		if err := worktree.Reset(&git.ResetOptions{Commit: previous, Mode: git.MixedReset}); err != nil {
			return err
		}
		previousFiles, err := gitCommitSnapshot(repository, previous)
		if err != nil {
			return err
		}
		if err := replaceGitWorktree(root, previousFiles); err != nil {
			return err
		}
		if err := saveState(root, mergeState.PreviousState); err != nil {
			return err
		}
		if err := clearMergeState(root); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Merge aborted; restored the pre-merge Git refs, index, and worktree.")
		return nil
	}
	if !continueMerge {
		return errors.New("usage: gew merge (--abort | --continue [-m MESSAGE])")
	}
	if err := validateMergeResolved(root, mergeState); err != nil {
		return err
	}
	if message == "" {
		message = mergeState.Message
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	author, err := gitAuthor(repository, "", "")
	if err != nil {
		return err
	}
	oid, err := worktree.Commit(message, &git.CommitOptions{Author: author, Committer: author, AllowEmptyCommits: true})
	if err != nil {
		return err
	}
	if err := clearMergeState(root); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "[%s %.12s] %s\n", state.Branch, oid, message)
	return nil
}

func pendingGitCommits(repository *git.Repository, trackingRef string) ([]*object.Commit, error) {
	head, err := repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	trackingOID := plumbing.ZeroHash
	if trackingRef != "" {
		tracking, refErr := repository.Reference(plumbing.ReferenceName(trackingRef), true)
		if refErr == nil {
			trackingOID = tracking.Hash()
		} else if !errors.Is(refErr, plumbing.ErrReferenceNotFound) {
			return nil, refErr
		}
	}
	commits := make([]*object.Commit, 0)
	current, err := repository.CommitObject(head.Hash())
	for err == nil && current.Hash != trackingOID {
		if len(current.ParentHashes) > 2 {
			return nil, fmt.Errorf("octopus commit %s cannot be exported", current.Hash)
		}
		commits = append(commits, current)
		if len(current.ParentHashes) == 0 {
			current = nil
			break
		}
		current, err = repository.CommitObject(current.ParentHashes[0])
	}
	if err != nil {
		return nil, err
	}
	if !trackingOID.IsZero() && (current == nil || current.Hash != trackingOID) {
		return nil, errors.New("Gew tracking commit is not a first-parent ancestor of HEAD")
	}
	for left, right := 0, len(commits)-1; left < right; left, right = left+1, right-1 {
		commits[left], commits[right] = commits[right], commits[left]
	}
	return commits, nil
}

func gitRemoteChanges(repository *git.Repository, commit *object.Commit, remoteFiles map[string]RemoteFile) ([]RemoteChange, error) {
	after, err := gitCommitSnapshot(repository, commit.Hash)
	if err != nil {
		return nil, err
	}
	before := make(map[string][]byte)
	if len(commit.ParentHashes) > 0 {
		before, err = gitCommitSnapshot(repository, commit.ParentHashes[0])
		if err != nil {
			return nil, err
		}
	}
	changes := byteSnapshotChanges(before, after)
	result := make([]RemoteChange, 0, len(changes))
	for _, item := range changes {
		operation := "update"
		switch item.Kind {
		case "created":
			operation = "create"
		case "deleted":
			operation = "delete"
		}
		change := RemoteChange{Path: item.Path, Operation: operation}
		if item.Kind != "deleted" {
			change.Content = after[item.Path]
		}
		if remote, ok := remoteFiles[item.Path]; ok {
			change.BlobID = remote.BlobID
			change.LastCommitID = remote.LastCommitID
		}
		result = append(result, change)
	}
	return result, nil
}

func exportDigest(changes []RemoteChange) string {
	hash := sha256.New()
	for _, change := range changes {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", change.Operation, change.Path, len(change.Content))
		hash.Write(change.Content)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func exportPreparedPath(root string) string {
	return filepath.Join(root, ".gew", "export-prepared.json")
}

func savePreparedGitExport(root string, prepared preparedGitExport) error {
	data, err := json.MarshalIndent(prepared, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(exportPreparedPath(root), append(data, '\n'), 0o600)
}

func loadPreparedGitExport(root string) (*preparedGitExport, error) {
	data, err := os.ReadFile(exportPreparedPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var prepared preparedGitExport
	if err := json.Unmarshal(data, &prepared); err != nil {
		return nil, err
	}
	if prepared.Version != exportReceiptVersion || prepared.LocalOID == "" {
		return nil, errors.New("invalid prepared Git export journal")
	}
	return &prepared, nil
}

func receiptPath(root, oid string) string {
	return filepath.Join(root, ".gew", "exports", oid+".json")
}

func saveGitExportReceipt(root string, receipt gitExportReceipt) error {
	if len(receipt.LocalOID) != 40 {
		return errors.New("invalid local OID for export receipt")
	}
	if _, err := hex.DecodeString(receipt.LocalOID); err != nil {
		return errors.New("invalid local OID for export receipt")
	}
	if err := os.MkdirAll(filepath.Dir(receiptPath(root, receipt.LocalOID)), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(receiptPath(root, receipt.LocalOID), append(data, '\n'), 0o600)
}

func verifyRemoteTree(ctx context.Context, remote Forge, ref RepositoryRef, commit string, expected map[string][]byte) error {
	files, err := remote.Tree(ctx, ref, commit)
	if err != nil {
		return err
	}
	if len(files) != len(expected) {
		return fmt.Errorf("provider commit %s tree has %d files, expected %d", commit, len(files), len(expected))
	}
	archive, err := remote.Snapshot(ctx, ref, commit)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "gew-verify-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err := extractArchive(archive, directory); err != nil {
		return err
	}
	actual, err := gitWorktreeSnapshot(directory)
	if err != nil {
		return err
	}
	for filePath, content := range expected {
		if _, exists := files[filePath]; !exists {
			return fmt.Errorf("provider commit %s is missing %s", commit, filePath)
		}
		if !bytes.Equal(actual[filePath], content) {
			return fmt.Errorf("provider commit %s has unexpected bytes for %s", commit, filePath)
		}
	}
	return nil
}

func advanceGitTracking(repository *git.Repository, trackingName string, oldOID, newOID plumbing.Hash) error {
	name := plumbing.ReferenceName(trackingName)
	next := plumbing.NewHashReference(name, newOID)
	if oldOID.IsZero() {
		if _, err := repository.Reference(name, true); err == nil {
			return errors.New("private tracking ref appeared concurrently")
		} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return err
		}
		return repository.Storer.SetReference(next)
	}
	return repository.Storer.CheckAndSetReference(next, plumbing.NewHashReference(name, oldOID))
}

func (a app) gitPush(root string, state workspaceState, newBranch string) error {
	remote, err := forgeForWorkspace(state)
	if err != nil {
		return err
	}
	return a.gitPushWithForge(root, state, newBranch, remote)
}

func (a app) gitPushWithForge(root string, state workspaceState, newBranch string, remote Forge) error {
	repository, _, err := openGitWorkspace(root, state)
	if err != nil {
		return err
	}
	if !remote.Capabilities().Push {
		return fmt.Errorf("%s push is disabled because its concurrency safety has not been verified: %w", remote.Kind(), ErrUnsupported)
	}
	if newBranch != "" && !remote.Capabilities().BranchCreate {
		return fmt.Errorf("%s does not support creating branches through gew: %w", remote.Kind(), ErrUnsupported)
	}
	pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Fprintln(a.out, "Everything up to date. No local commits to push.")
		return nil
	}
	remoteHead, err := remote.Head(context.Background(), state.Remote, state.Branch)
	if err != nil {
		if isRemoteNotFound(err) && state.BaseCommit == "" {
			remoteHead = ""
		} else {
			return err
		}
	}
	prepared, err := loadPreparedGitExport(root)
	if err != nil {
		return err
	}
	if prepared != nil && prepared.TargetBranch != state.Branch {
		targetHead, targetErr := remote.Head(context.Background(), state.Remote, prepared.TargetBranch)
		if targetErr == nil && targetHead != prepared.ExpectedProvider {
			reconciled, reconcileErr := reconcilePreparedGitExport(context.Background(), root, &state, repository, remote, targetHead, prepared)
			if reconcileErr != nil {
				return reconcileErr
			}
			if !reconciled {
				return fmt.Errorf("new branch %s exists but does not contain prepared local commit %.12s", prepared.TargetBranch, prepared.LocalOID)
			}
			if err := finalizeGitNewBranch(repository, &state, prepared.TargetBranch, plumbing.NewHash(prepared.LocalOID)); err != nil {
				return err
			}
			if err := saveState(root, state); err != nil {
				return err
			}
			pending, err = pendingGitCommits(repository, state.Hybrid.TrackingRef)
			if err != nil {
				return err
			}
			if len(pending) == 0 {
				fmt.Fprintln(a.out, "Reconciled the previously accepted new-branch commit. Everything is up to date.")
				return nil
			}
		} else if targetErr != nil && !isRemoteNotFound(targetErr) {
			return targetErr
		}
	}
	if prepared != nil && remoteHead != prepared.ExpectedProvider {
		reconciled, reconcileErr := reconcilePreparedGitExport(context.Background(), root, &state, repository, remote, remoteHead, prepared)
		if reconcileErr != nil {
			return reconcileErr
		}
		if !reconciled {
			return fmt.Errorf("remote branch advanced while local commit %.12s has an unresolved export journal; synchronize manually", prepared.LocalOID)
		}
		pending, err = pendingGitCommits(repository, state.Hybrid.TrackingRef)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Fprintln(a.out, "Reconciled the previously accepted remote commit. Everything is up to date.")
			return nil
		}
	}
	if remoteHead != state.BaseCommit {
		return fmt.Errorf("remote branch advanced from %.12s to %.12s; run 'gew pull' before pushing", state.BaseCommit, remoteHead)
	}
	remoteFiles := make(map[string]RemoteFile)
	if remoteHead != "" {
		remoteFiles, err = remote.Tree(context.Background(), state.Remote, remoteHead)
		if err != nil {
			return err
		}
	}
	oldTracking := plumbing.ZeroHash
	if state.Hybrid.LastLocalOID != "" {
		oldTracking = plumbing.NewHash(state.Hybrid.LastLocalOID)
	}
	targetBranch := state.Branch
	if newBranch != "" {
		targetBranch = newBranch
	}
	for index, commit := range pending {
		changes, err := gitRemoteChanges(repository, commit, remoteFiles)
		if err != nil {
			return err
		}
		paths := make([]string, len(changes))
		for i, change := range changes {
			paths[i] = change.Path
		}
		prepared := preparedGitExport{
			Version: exportReceiptVersion, LocalOID: commit.Hash.String(), ExpectedProvider: remoteHead,
			TargetBranch: targetBranch, Message: commit.Message, Paths: paths, Digest: exportDigest(changes),
		}
		if err := savePreparedGitExport(root, prepared); err != nil {
			return err
		}
		request := ApplyCommitRequest{
			Repository: state.Remote, Branch: state.Branch, ExpectedHead: remoteHead,
			Message: strings.TrimSpace(commit.Message), Changes: changes,
		}
		if index == 0 && newBranch != "" {
			request.NewBranch = targetBranch
		} else if index > 0 {
			request.Branch = targetBranch
		}
		result, err := remote.ApplyCommit(context.Background(), request)
		if err != nil {
			return fmt.Errorf("export of local commit %.12s remains prepared for reconciliation: %w", commit.Hash, err)
		}
		if err := validateApplyResult(request, result); err != nil {
			return err
		}
		confirmedHead, err := remote.Head(context.Background(), state.Remote, targetBranch)
		if err != nil {
			return fmt.Errorf("refresh provider head after exporting %.12s: %w", result.CommitID, err)
		}
		if confirmedHead != result.CommitID {
			return fmt.Errorf("provider did not confirm exported commit %.12s on %s (head %.12s)", result.CommitID, targetBranch, confirmedHead)
		}
		expectedTree, err := gitCommitSnapshot(repository, commit.Hash)
		if err != nil {
			return err
		}
		if err := verifyRemoteTree(context.Background(), remote, state.Remote, result.CommitID, expectedTree); err != nil {
			return err
		}
		receipt := gitExportReceipt{
			Version: exportReceiptVersion, LocalOID: commit.Hash.String(), ProviderID: result.CommitID,
			ProviderBase: remoteHead, Message: commit.Message, Paths: paths, Linearized: len(commit.ParentHashes) == 2,
		}
		if err := saveGitExportReceipt(root, receipt); err != nil {
			return err
		}
		if err := advanceGitTracking(repository, state.Hybrid.TrackingRef, oldTracking, commit.Hash); err != nil {
			return fmt.Errorf("receipt saved but tracking ref did not advance; rerun push for recovery: %w", err)
		}
		oldTracking = commit.Hash
		remoteHead = result.CommitID
		state.BaseCommit = result.CommitID
		state.Hybrid.LastLocalOID = commit.Hash.String()
		state.Hybrid.LastProviderID = result.CommitID
		if err := saveState(root, state); err != nil {
			return err
		}
		if err := os.Remove(exportPreparedPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		remoteFiles, err = remote.Tree(context.Background(), state.Remote, remoteHead)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Exported %.12s as remote %.12s\n", commit.Hash, result.CommitID)
	}
	if newBranch != "" {
		if err := finalizeGitNewBranch(repository, &state, targetBranch, oldTracking); err != nil {
			return err
		}
		if err := saveState(root, state); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "Pushed %d local Git commit(s) to %s.\n", len(pending), targetBranch)
	return nil
}

func finalizeGitNewBranch(repository *git.Repository, state *workspaceState, targetBranch string, oid plumbing.Hash) error {
	trackingName, err := gewTrackingRef(state.Provider, targetBranch)
	if err != nil {
		return err
	}
	if err := repository.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(trackingName), oid)); err != nil {
		return err
	}
	localBranch := plumbing.NewBranchReferenceName(targetBranch)
	if err := localBranch.Validate(); err != nil {
		return err
	}
	if err := repository.Storer.SetReference(plumbing.NewHashReference(localBranch, oid)); err != nil {
		return err
	}
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, localBranch)); err != nil {
		return err
	}
	state.Branch = targetBranch
	state.Hybrid.TrackingRef = trackingName
	state.Hybrid.BranchRef = localBranch.String()
	return nil
}

func reconcilePreparedGitExport(ctx context.Context, root string, state *workspaceState, repository *git.Repository, remote Forge, remoteHead string, prepared *preparedGitExport) (bool, error) {
	commit, err := repository.CommitObject(plumbing.NewHash(prepared.LocalOID))
	if err != nil {
		return false, err
	}
	details, err := remote.CommitDetails(ctx, state.Remote, remoteHead)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(details.Message) != strings.TrimSpace(prepared.Message) {
		return false, nil
	}
	if prepared.ExpectedProvider == "" {
		if len(details.ParentIDs) != 0 {
			return false, nil
		}
	} else if len(details.ParentIDs) == 0 || details.ParentIDs[0] != prepared.ExpectedProvider {
		return false, nil
	}
	expectedPaths := append([]string(nil), prepared.Paths...)
	actualPaths := append([]string(nil), details.Paths...)
	sort.Strings(expectedPaths)
	sort.Strings(actualPaths)
	if strings.Join(expectedPaths, "\x00") != strings.Join(actualPaths, "\x00") {
		return false, nil
	}
	expectedTree, err := gitCommitSnapshot(repository, commit.Hash)
	if err != nil {
		return false, err
	}
	if err := verifyRemoteTree(ctx, remote, state.Remote, remoteHead, expectedTree); err != nil {
		return false, nil
	}
	receipt := gitExportReceipt{
		Version: exportReceiptVersion, LocalOID: commit.Hash.String(), ProviderID: remoteHead,
		ProviderBase: prepared.ExpectedProvider, Message: commit.Message, Paths: expectedPaths, Linearized: len(commit.ParentHashes) == 2,
	}
	if err := saveGitExportReceipt(root, receipt); err != nil {
		return false, err
	}
	oldOID := plumbing.ZeroHash
	if state.Hybrid.LastLocalOID != "" {
		oldOID = plumbing.NewHash(state.Hybrid.LastLocalOID)
	}
	if err := advanceGitTracking(repository, state.Hybrid.TrackingRef, oldOID, commit.Hash); err != nil {
		return false, err
	}
	state.BaseCommit = remoteHead
	state.Hybrid.LastProviderID = remoteHead
	state.Hybrid.LastLocalOID = commit.Hash.String()
	if err := saveState(root, *state); err != nil {
		return false, err
	}
	if err := os.Remove(exportPreparedPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

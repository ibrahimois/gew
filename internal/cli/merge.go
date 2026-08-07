package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mergecore "gew/internal/merge"
)

const mergeStateVersion = 1

type mergeConflict struct {
	Path   string `json:"path"`
	Binary bool   `json:"binary,omitempty"`
}

type activeMerge struct {
	Version           int                  `json:"version"`
	RemoteCommit      string               `json:"remote_commit"`
	Message           string               `json:"message"`
	PreviousState     workspaceState       `json:"previous_state"`
	OursFiles         map[string]fileState `json:"ours_files"`
	Conflicts         []mergeConflict      `json:"conflicts"`
	GitPreviousHead   string               `json:"git_previous_head,omitempty"`
	GitRemoteAnchor   string               `json:"git_remote_anchor,omitempty"`
	GitTrackingBefore string               `json:"git_tracking_before,omitempty"`
}

type optionalContent = mergecore.Content

func (a app) mergeOperation(ctx context.Context, options mergeOptions) error {
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if state.Backend == WorkspaceGit {
		return a.gitMerge(root, state, options.Abort, options.Continue, strings.TrimSpace(options.Message))
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState == nil {
		return errors.New("no merge is in progress")
	}
	if options.Abort {
		if err := restoreSnapshot(root, mergeState.OursFiles); err != nil {
			return err
		}
		if err := saveState(root, mergeState.PreviousState); err != nil {
			return err
		}
		if err := saveIndex(root, stageIndex{Version: indexVersion, Entries: make(map[string]indexEntry)}); err != nil {
			return err
		}
		if err := clearMergeState(root); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Merge aborted; restored the pre-merge workspace and local queue.")
		return nil
	}
	if err := validateMergeResolved(root, mergeState); err != nil {
		return err
	}
	commitMessage := strings.TrimSpace(options.Message)
	if commitMessage == "" {
		commitMessage = mergeState.Message
	}
	if err := a.addOperation(ctx, addOptions{All: true}); err != nil {
		return err
	}
	if err := a.commitOperation(ctx, commitOptions{Message: commitMessage}); err != nil {
		return err
	}
	_ = state
	return nil
}

func validateMergeResolved(root string, mergeState *activeMerge) error {
	for _, conflict := range mergeState.Conflicts {
		if conflict.Binary {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(conflict.Path))); err != nil {
				return fmt.Errorf("resolve binary conflict %s before continuing: %w", conflict.Path, err)
			}
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(conflict.Path)))
		if err != nil {
			return fmt.Errorf("resolve conflict %s before continuing: %w", conflict.Path, err)
		}
		if containsConflictMarkerLine(content) {
			return fmt.Errorf("conflict markers remain in %s", conflict.Path)
		}
	}
	return nil
}

func containsConflictMarkerLine(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "<<<<<<< ours" || line == "||||||| base" || line == "=======" || line == ">>>>>>> theirs" {
			return true
		}
	}
	return false
}

func (a app) mergeRemote(ctx context.Context, root string, state workspaceState, remote Forge, remoteCommit string, hadWorkingChanges bool) error {
	baseDirectory, err := os.MkdirTemp("", "gew-merge-base-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(baseDirectory)
	theirsDirectory, err := os.MkdirTemp("", "gew-merge-theirs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(theirsDirectory)
	resultDirectory, err := os.MkdirTemp("", "gew-merge-result-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(resultDirectory)

	if state.BaseCommit != "" {
		baseArchive, err := forgeSnapshot(ctx, remote, state.Remote, state.BaseCommit)
		if err != nil {
			return fmt.Errorf("download merge base %.12s: %w", state.BaseCommit, err)
		}
		if err := extractArchive(baseArchive, baseDirectory); err != nil {
			return err
		}
	}
	theirsArchive, err := forgeSnapshot(ctx, remote, state.Remote, remoteCommit)
	if err != nil {
		return err
	}
	if err := extractArchive(theirsArchive, theirsDirectory); err != nil {
		return err
	}
	remoteTree, err := remote.Tree(ctx, state.Remote, remoteCommit)
	if err != nil {
		return err
	}
	oursFiles, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	for filePath, metadata := range oursFiles {
		if err := storeObjectFromFile(root, filePath, metadata.Hash); err != nil {
			return err
		}
	}
	theirsFiles, err := scanWorkspace(theirsDirectory)
	if err != nil {
		return err
	}
	if err := storeObjectsFromDirectory(root, theirsDirectory, theirsFiles); err != nil {
		return err
	}
	conflicts, err := mergeDirectories(root, baseDirectory, theirsDirectory, resultDirectory)
	if err != nil {
		return err
	}
	previousState := state
	mergeState := activeMerge{
		Version: mergeStateVersion, RemoteCommit: remoteCommit,
		Message:       fmt.Sprintf("Merge remote %s into local changes", state.Branch),
		PreviousState: previousState, OursFiles: oursFiles, Conflicts: conflicts,
	}
	if len(conflicts) > 0 {
		if err := saveMergeState(root, mergeState); err != nil {
			return err
		}
	}
	if err := replaceWorkspaceFiles(root, resultDirectory); err != nil {
		return err
	}
	state.BaseCommit = remoteCommit
	state.Files = mergeFileMetadata(theirsFiles, remoteBlobIDs(remoteTree))
	state.Queue = nil
	state.LocalHead = ""
	if err := saveState(root, state); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		if err := writeBinaryConflictSides(root, baseDirectory, oursFiles, theirsDirectory, conflicts); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Merge stopped with %d conflict(s). Resolve them, then run 'gew merge --continue'.\n", len(conflicts))
		for _, conflict := range conflicts {
			kind := "text"
			if conflict.Binary {
				kind = "binary"
			}
			fmt.Fprintf(a.out, "  %-6s %s\n", kind, conflict.Path)
		}
		return errors.New("automatic merge failed; conflicts require resolution")
	}
	if len(previousState.Queue) > 0 {
		if err := a.addOperation(ctx, addOptions{All: true}); err != nil {
			return err
		}
		index, err := loadIndex(root)
		if err != nil {
			return err
		}
		if len(index.Entries) > 0 {
			if err := a.commitOperation(ctx, commitOptions{Message: mergeState.Message}); err != nil {
				return err
			}
			_, updatedState, err := findWorkspace()
			if err != nil {
				return err
			}
			if err := markCommitsSuperseded(root, previousState.Queue, updatedState.LocalHead); err != nil {
				return err
			}
		} else if err := markCommitsSuperseded(root, previousState.Queue, "remote merge"); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Merged remote %s and replaced %d queued commit(s) with a local merge commit.\n", state.Branch, len(previousState.Queue))
		return nil
	}
	if hadWorkingChanges {
		fmt.Fprintf(a.out, "Merged remote %s into the working tree; local changes remain unstaged.\n", state.Branch)
	} else {
		fmt.Fprintf(a.out, "Merged remote %s.\n", state.Branch)
	}
	return nil
}

func mergeDirectories(oursDirectory, baseDirectory, theirsDirectory, resultDirectory string) ([]mergeConflict, error) {
	baseFiles, err := scanWorkspace(baseDirectory)
	if err != nil {
		return nil, err
	}
	oursFiles, err := scanWorkspace(oursDirectory)
	if err != nil {
		return nil, err
	}
	theirsFiles, err := scanWorkspace(theirsDirectory)
	if err != nil {
		return nil, err
	}
	paths := unionPaths(baseFiles, oursFiles)
	pathSet := make(map[string]struct{}, len(paths)+len(theirsFiles))
	for _, filePath := range paths {
		pathSet[filePath] = struct{}{}
	}
	for filePath := range theirsFiles {
		pathSet[filePath] = struct{}{}
	}
	paths = paths[:0]
	for filePath := range pathSet {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	conflicts := make([]mergeConflict, 0)
	for _, filePath := range paths {
		base, err := readOptionalContent(baseDirectory, filePath)
		if err != nil {
			return nil, err
		}
		ours, err := readOptionalContent(oursDirectory, filePath)
		if err != nil {
			return nil, err
		}
		theirs, err := readOptionalContent(theirsDirectory, filePath)
		if err != nil {
			return nil, err
		}
		merged, conflicted, binaryConflict := mergeFile(base, ours, theirs)
		if merged.Exists {
			target := filepath.Join(resultDirectory, filepath.FromSlash(filePath))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			mode := merged.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(target, merged.Content, mode.Perm()); err != nil {
				return nil, err
			}
		}
		if conflicted {
			conflicts = append(conflicts, mergeConflict{Path: filePath, Binary: binaryConflict})
		}
	}
	return conflicts, nil
}

func mergeFile(base, ours, theirs optionalContent) (optionalContent, bool, bool) {
	return mergecore.File(base, ours, theirs)
}

func mergeText(baseContent, oursContent, theirsContent []byte) ([]byte, bool) {
	return mergecore.Text(baseContent, oursContent, theirsContent)
}

func isBinary(content []byte) bool {
	return mergecore.IsBinary(content)
}

func readOptionalContent(directory, filePath string) (optionalContent, error) {
	target := filepath.Join(directory, filepath.FromSlash(filePath))
	content, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return optionalContent{}, nil
	}
	if err != nil {
		return optionalContent{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return optionalContent{}, err
	}
	return optionalContent{Exists: true, Content: content, Mode: info.Mode().Perm()}, nil
}

func storeObjectsFromDirectory(root, source string, files map[string]fileState) error {
	for filePath, metadata := range files {
		content, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(filePath)))
		if err != nil {
			return err
		}
		if err := storeObject(root, metadata.Hash, content); err != nil {
			return err
		}
	}
	return nil
}

func replaceWorkspaceFiles(root, source string) error {
	current, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	return replaceTrackedFiles(root, source, current)
}

func restoreSnapshot(root string, files map[string]fileState) error {
	temporary, err := os.MkdirTemp("", "gew-merge-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for filePath, metadata := range files {
		content, err := os.ReadFile(objectPath(root, metadata.Hash))
		if err != nil {
			return err
		}
		target := filepath.Join(temporary, filepath.FromSlash(filePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(metadata.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, content, mode.Perm()); err != nil {
			return err
		}
	}
	return replaceWorkspaceFiles(root, temporary)
}

func mergeStatePath(root string) string {
	return filepath.Join(root, ".gew", "merge.json")
}

func saveMergeState(root string, state activeMerge) error {
	state.Version = mergeStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(mergeStatePath(root), append(data, '\n'), 0o600)
}

func loadMergeState(root string) (*activeMerge, error) {
	data, err := os.ReadFile(mergeStatePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state activeMerge
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Version != mergeStateVersion {
		return nil, fmt.Errorf("unsupported merge state version %d", state.Version)
	}
	return &state, nil
}

func clearMergeState(root string) error {
	if err := os.Remove(mergeStatePath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(filepath.Join(root, ".gew", "conflicts"))
}

func writeBinaryConflictSides(root, baseDirectory string, oursFiles map[string]fileState, theirsDirectory string, conflicts []mergeConflict) error {
	for _, conflict := range conflicts {
		if !conflict.Binary {
			continue
		}
		conflictRoot := filepath.Join(root, ".gew", "conflicts", filepath.FromSlash(conflict.Path))
		if err := os.MkdirAll(filepath.Dir(conflictRoot), 0o700); err != nil {
			return err
		}
		base, _ := readOptionalContent(baseDirectory, conflict.Path)
		oursMetadata, oursExists := oursFiles[conflict.Path]
		var ours []byte
		if oursExists {
			ours, _ = os.ReadFile(objectPath(root, oursMetadata.Hash))
		}
		theirs, _ := readOptionalContent(theirsDirectory, conflict.Path)
		for suffix, content := range map[string][]byte{".base": base.Content, ".ours": ours, ".theirs": theirs.Content} {
			if err := os.WriteFile(conflictRoot+suffix, content, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func markCommitsSuperseded(root string, commitIDs []string, replacement string) error {
	for _, id := range commitIDs {
		commit, err := loadLocalCommit(root, id)
		if err != nil {
			return err
		}
		commit.SupersededBy = replacement
		if err := saveLocalCommit(root, commit); err != nil {
			return err
		}
	}
	return nil
}

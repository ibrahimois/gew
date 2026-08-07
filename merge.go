package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
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

type optionalContent struct {
	Exists  bool
	Content []byte
	Mode    fs.FileMode
}

type baseEdit struct {
	start       int
	end         int
	replacement []string
	side        byte
}

type editCluster struct {
	start int
	end   int
	edits []baseEdit
}

func (a app) merge(args []string) error {
	flags := flag.NewFlagSet("merge", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	abort := flags.Bool("abort", false, "restore the pre-merge workspace")
	continueMerge := flags.Bool("continue", false, "stage resolved files and commit")
	message := flags.String("m", "", "merge commit message")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*abort == *continueMerge) {
		return errors.New("usage: gew merge (--abort | --continue [-m MESSAGE])")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if state.Backend == WorkspaceGit {
		return a.gitMerge(root, state, *abort, *continueMerge, strings.TrimSpace(*message))
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState == nil {
		return errors.New("no merge is in progress")
	}
	if *abort {
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
	commitMessage := strings.TrimSpace(*message)
	if commitMessage == "" {
		commitMessage = mergeState.Message
	}
	if err := a.add([]string{"-A"}); err != nil {
		return err
	}
	if err := a.commit([]string{"-m", commitMessage}); err != nil {
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

func (a app) mergeRemote(root string, state workspaceState, remote Forge, remoteCommit string, hadWorkingChanges bool) error {
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
		baseArchive, err := forgeSnapshot(context.Background(), remote, state.Remote, state.BaseCommit)
		if err != nil {
			return fmt.Errorf("download merge base %.12s: %w", state.BaseCommit, err)
		}
		if err := extractArchive(baseArchive, baseDirectory); err != nil {
			return err
		}
	}
	theirsArchive, err := forgeSnapshot(context.Background(), remote, state.Remote, remoteCommit)
	if err != nil {
		return err
	}
	if err := extractArchive(theirsArchive, theirsDirectory); err != nil {
		return err
	}
	remoteTree, err := remote.Tree(context.Background(), state.Remote, remoteCommit)
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
		if err := a.add([]string{"-A"}); err != nil {
			return err
		}
		index, err := loadIndex(root)
		if err != nil {
			return err
		}
		if len(index.Entries) > 0 {
			if err := a.commit([]string{"-m", mergeState.Message}); err != nil {
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
	if optionalEqual(ours, theirs) {
		return ours, false, false
	}
	if optionalEqual(ours, base) {
		return theirs, false, false
	}
	if optionalEqual(theirs, base) {
		return ours, false, false
	}
	binary := isBinary(base.Content) || isBinary(ours.Content) || isBinary(theirs.Content)
	if binary {
		result := ours
		if !result.Exists {
			result = theirs
		}
		return result, true, true
	}
	merged, conflict := mergeText(base.Content, ours.Content, theirs.Content)
	mode := ours.Mode
	if mode == 0 {
		mode = theirs.Mode
	}
	return optionalContent{Exists: true, Content: merged, Mode: mode}, conflict, false
}

func mergeText(baseContent, oursContent, theirsContent []byte) ([]byte, bool) {
	baseLines := splitDiffLines(string(baseContent))
	oursLines := splitDiffLines(string(oursContent))
	theirsLines := splitDiffLines(string(theirsContent))
	oursEdits := editsFromBase(baseLines, oursLines, 'o')
	theirsEdits := editsFromBase(baseLines, theirsLines, 't')
	clusters := clusterEdits(append(oursEdits, theirsEdits...))
	result := make([]string, 0, len(baseLines)+len(oursLines)+len(theirsLines))
	position := 0
	conflicted := false
	for _, cluster := range clusters {
		if cluster.start > position {
			result = append(result, baseLines[position:cluster.start]...)
		}
		oursRegion := applyCluster(baseLines, cluster, 'o')
		theirsRegion := applyCluster(baseLines, cluster, 't')
		baseRegion := append([]string(nil), baseLines[cluster.start:cluster.end]...)
		switch {
		case stringSlicesEqual(oursRegion, theirsRegion):
			result = append(result, oursRegion...)
		case stringSlicesEqual(oursRegion, baseRegion):
			result = append(result, theirsRegion...)
		case stringSlicesEqual(theirsRegion, baseRegion):
			result = append(result, oursRegion...)
		default:
			conflicted = true
			result = append(result, "<<<<<<< ours\n")
			result = append(result, ensureFinalNewline(oursRegion)...)
			result = append(result, "||||||| base\n")
			result = append(result, ensureFinalNewline(baseRegion)...)
			result = append(result, "=======\n")
			result = append(result, ensureFinalNewline(theirsRegion)...)
			result = append(result, ">>>>>>> theirs\n")
		}
		position = cluster.end
	}
	result = append(result, baseLines[position:]...)
	return []byte(strings.Join(result, "")), conflicted
}

func editsFromBase(base, variant []string, side byte) []baseEdit {
	operations := lineDiff(base, variant)
	edits := make([]baseEdit, 0)
	basePosition := 0
	for index := 0; index < len(operations); {
		if operations[index].prefix == ' ' {
			basePosition++
			index++
			continue
		}
		start := basePosition
		replacement := make([]string, 0)
		for index < len(operations) && operations[index].prefix != ' ' {
			if operations[index].prefix == '-' {
				basePosition++
			} else {
				replacement = append(replacement, operations[index].line)
			}
			index++
		}
		edits = append(edits, baseEdit{start: start, end: basePosition, replacement: replacement, side: side})
	}
	return edits
}

func clusterEdits(edits []baseEdit) []editCluster {
	if len(edits) == 0 {
		return nil
	}
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start == edits[j].start {
			if edits[i].end == edits[j].end {
				return edits[i].side < edits[j].side
			}
			return edits[i].end < edits[j].end
		}
		return edits[i].start < edits[j].start
	})
	clusters := make([]editCluster, 0)
	for _, edit := range edits {
		if len(clusters) == 0 {
			clusters = append(clusters, editCluster{start: edit.start, end: edit.end, edits: []baseEdit{edit}})
			continue
		}
		last := &clusters[len(clusters)-1]
		overlaps := edit.start < last.end || (edit.start == last.start && (edit.start == edit.end || last.start == last.end))
		if overlaps {
			last.edits = append(last.edits, edit)
			if edit.end > last.end {
				last.end = edit.end
			}
			continue
		}
		clusters = append(clusters, editCluster{start: edit.start, end: edit.end, edits: []baseEdit{edit}})
	}
	return clusters
}

func applyCluster(base []string, cluster editCluster, side byte) []string {
	selected := make([]baseEdit, 0)
	for _, edit := range cluster.edits {
		if edit.side == side {
			selected = append(selected, edit)
		}
	}
	if len(selected) == 0 {
		return append([]string(nil), base[cluster.start:cluster.end]...)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].start < selected[j].start })
	result := make([]string, 0)
	position := cluster.start
	for _, edit := range selected {
		if edit.start > position {
			result = append(result, base[position:edit.start]...)
		}
		result = append(result, edit.replacement...)
		if edit.end > position {
			position = edit.end
		}
	}
	if position < cluster.end {
		result = append(result, base[position:cluster.end]...)
	}
	return result
}

func optionalEqual(left, right optionalContent) bool {
	return left.Exists == right.Exists && (!left.Exists || bytes.Equal(left.Content, right.Content))
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func ensureFinalNewline(lines []string) []string {
	result := append([]string(nil), lines...)
	if len(result) > 0 && !strings.HasSuffix(result[len(result)-1], "\n") {
		result[len(result)-1] += "\n"
	}
	return result
}

func isBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
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

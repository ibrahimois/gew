package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const indexVersion = 1

type indexEntry struct {
	Kind   string `json:"kind"`
	Object string `json:"object,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type stageIndex struct {
	Version int                   `json:"version"`
	Entries map[string]indexEntry `json:"entries"`
}

type commitChange struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Object string `json:"object,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type localCommit struct {
	ID           string         `json:"id"`
	Parent       string         `json:"parent,omitempty"`
	Message      string         `json:"message"`
	CreatedAt    time.Time      `json:"created_at"`
	Changes      []commitChange `json:"changes"`
	RemoteSHA    string         `json:"remote_sha,omitempty"`
	PushedAt     *time.Time     `json:"pushed_at,omitempty"`
	SupersededBy string         `json:"superseded_by,omitempty"`
}

type pathSelector struct {
	path      string
	recursive bool
}

func (a app) add(args []string) error {
	flags := flag.NewFlagSet("add", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	allShort := flags.Bool("A", false, "stage all changes")
	allLong := flags.Bool("all", false, "stage all changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*allShort && !*allLong && flags.NArg() == 0 {
		return errors.New("usage: gew add [-A|--all] PATH...")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if err := ensureBaselineObjects(root, state.Files); err != nil {
		return err
	}
	selectors, err := selectorsForArgs(root, flags.Args(), *allShort || *allLong)
	if err != nil {
		return err
	}
	current, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	candidates := unionPaths(state.Files, current)
	for filePath := range index.Entries {
		found := false
		for _, candidate := range candidates {
			if candidate == filePath {
				found = true
				break
			}
		}
		if !found {
			candidates = append(candidates, filePath)
		}
	}
	sort.Strings(candidates)
	staged := 0
	matched := make(map[int]bool)
	for _, filePath := range candidates {
		selected, selectorIndex := matchesSelectors(filePath, selectors)
		if !selected {
			continue
		}
		for _, indexValue := range selectorIndex {
			matched[indexValue] = true
		}
		before, existedBefore := state.Files[filePath]
		after, existsNow := current[filePath]
		switch {
		case existedBefore && !existsNow:
			index.Entries[filePath] = indexEntry{Kind: "deleted"}
			staged++
		case !existedBefore && existsNow:
			if err := storeObjectFromFile(root, filePath, after.Hash); err != nil {
				return err
			}
			index.Entries[filePath] = indexEntry{Kind: "created", Object: after.Hash, Mode: after.Mode}
			staged++
		case existedBefore && existsNow && before.Hash != after.Hash:
			if err := storeObjectFromFile(root, filePath, after.Hash); err != nil {
				return err
			}
			index.Entries[filePath] = indexEntry{Kind: "modified", Object: after.Hash, Mode: after.Mode}
			staged++
		default:
			delete(index.Entries, filePath)
		}
	}
	for selectorIndex, selector := range selectors {
		if !matched[selectorIndex] && selector.path != "" {
			if _, tracked := state.Files[selector.path]; !tracked {
				if _, exists := current[selector.path]; !exists {
					return fmt.Errorf("pathspec %q did not match any files", selector.path)
				}
			}
		}
	}
	if err := saveIndex(root, index); err != nil {
		return err
	}
	if staged == 0 {
		fmt.Fprintln(a.out, "No changes matched.")
	} else {
		fmt.Fprintf(a.out, "Staged %d file change(s).\n", staged)
	}
	return nil
}

func (a app) reset(args []string) error {
	flags := flag.NewFlagSet("reset", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, _, err := findWorkspace()
	if err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	if len(index.Entries) == 0 {
		fmt.Fprintln(a.out, "Nothing is staged.")
		return nil
	}
	removed := 0
	if flags.NArg() == 0 {
		removed = len(index.Entries)
		index.Entries = make(map[string]indexEntry)
	} else {
		selectors, err := selectorsForArgs(root, flags.Args(), false)
		if err != nil {
			return err
		}
		for filePath := range index.Entries {
			selected, _ := matchesSelectors(filePath, selectors)
			if selected {
				delete(index.Entries, filePath)
				removed++
			}
		}
	}
	if err := saveIndex(root, index); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Unstaged %d file change(s).\n", removed)
	return nil
}

func (a app) commit(args []string) error {
	flags := flag.NewFlagSet("commit", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	message := flags.String("m", "", "commit message")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*message) == "" {
		return errors.New("usage: gew commit -m MESSAGE")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	if len(index.Entries) == 0 {
		return errors.New("nothing staged; use 'gew add' first")
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState != nil {
		if err := validateMergeResolved(root, mergeState); err != nil {
			return err
		}
	}
	paths := sortedIndexPaths(index.Entries)
	changes := make([]commitChange, 0, len(paths))
	for _, filePath := range paths {
		entry := index.Entries[filePath]
		changes = append(changes, commitChange{
			Kind: entry.Kind, Path: filePath, Object: entry.Object, Mode: entry.Mode,
		})
	}
	parent := state.LocalHead
	if parent == "" {
		parent = state.BaseCommit
	}
	commit := localCommit{
		Parent: parent, Message: strings.TrimSpace(*message), CreatedAt: time.Now().UTC(), Changes: changes,
	}
	commit.ID, err = localCommitID(commit)
	if err != nil {
		return err
	}
	if err := saveLocalCommit(root, commit); err != nil {
		return err
	}
	for _, item := range changes {
		switch item.Kind {
		case "deleted":
			delete(state.Files, item.Path)
		case "created", "modified":
			state.Files[item.Path] = fileState{Hash: item.Object, Mode: item.Mode}
		default:
			return fmt.Errorf("unsupported staged change kind %q", item.Kind)
		}
	}
	state.LocalHead = commit.ID
	state.Queue = append(state.Queue, commit.ID)
	state.History = append(state.History, commit.ID)
	if err := saveState(root, state); err != nil {
		return err
	}
	if err := saveIndex(root, stageIndex{Version: indexVersion, Entries: make(map[string]indexEntry)}); err != nil {
		return err
	}
	mergeState, err = loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState != nil {
		if err := markCommitsSuperseded(root, mergeState.PreviousState.Queue, commit.ID); err != nil {
			return err
		}
		if err := clearMergeState(root); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "[%s %.12s] %s\n", state.Branch, commit.ID, commit.Message)
	fmt.Fprintf(a.out, " %d file change(s) committed locally.\n", len(commit.Changes))
	return nil
}

func (a app) log(args []string) error {
	flags := flag.NewFlagSet("log", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	oneline := flags.Bool("oneline", false, "one commit per line")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gew log [--oneline]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if len(state.History) == 0 {
		fmt.Fprintln(a.out, "No local gew commits yet.")
		return nil
	}
	for index := len(state.History) - 1; index >= 0; index-- {
		commit, err := loadLocalCommit(root, state.History[index])
		if err != nil {
			return err
		}
		label := "unpushed"
		if commit.RemoteSHA != "" {
			label = "pushed " + shortID(commit.RemoteSHA)
		} else if commit.SupersededBy != "" {
			label = "superseded by " + shortID(commit.SupersededBy)
		}
		if *oneline {
			fmt.Fprintf(a.out, "%.12s %-20s %s\n", commit.ID, "["+label+"]", firstLine(commit.Message))
			continue
		}
		fmt.Fprintf(a.out, "commit %s (%s)\nDate:   %s\n\n    %s\n\n", commit.ID, label, commit.CreatedAt.Format(time.RFC3339), strings.ReplaceAll(commit.Message, "\n", "\n    "))
	}
	return nil
}

func loadIndex(root string) (stageIndex, error) {
	indexPath := filepath.Join(root, ".gew", "index.json")
	data, err := os.ReadFile(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return stageIndex{Version: indexVersion, Entries: make(map[string]indexEntry)}, nil
	}
	if err != nil {
		return stageIndex{}, err
	}
	var index stageIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return stageIndex{}, fmt.Errorf("parse staging index: %w", err)
	}
	if index.Version != indexVersion {
		return stageIndex{}, fmt.Errorf("unsupported staging index version %d", index.Version)
	}
	if index.Entries == nil {
		index.Entries = make(map[string]indexEntry)
	}
	return index, nil
}

func saveIndex(root string, index stageIndex) error {
	index.Version = indexVersion
	if index.Entries == nil {
		index.Entries = make(map[string]indexEntry)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(root, ".gew", "index.json"), append(data, '\n'), 0o600)
}

func selectorsForArgs(root string, args []string, all bool) ([]pathSelector, error) {
	if all {
		return []pathSelector{{path: "", recursive: true}}, nil
	}
	current, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	selectors := make([]pathSelector, 0, len(args))
	for _, argument := range args {
		absolute := argument
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(current, argument)
		}
		absolute = filepath.Clean(absolute)
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("pathspec %q is outside the workspace", argument)
		}
		if relative == "." {
			relative = ""
		}
		relative = filepath.ToSlash(relative)
		if relative == ".gew" || strings.HasPrefix(relative, ".gew/") || relative == ".git" || strings.HasPrefix(relative, ".git/") {
			return nil, fmt.Errorf("pathspec %q targets internal metadata", argument)
		}
		recursive := relative == ""
		if info, statErr := os.Stat(absolute); statErr == nil {
			recursive = info.IsDir()
		} else if errors.Is(statErr, os.ErrNotExist) {
			recursive = true
		} else {
			return nil, statErr
		}
		selectors = append(selectors, pathSelector{path: relative, recursive: recursive})
	}
	return selectors, nil
}

func matchesSelectors(filePath string, selectors []pathSelector) (bool, []int) {
	matched := make([]int, 0, 1)
	for index, selector := range selectors {
		if filePath == selector.path || (selector.recursive && (selector.path == "" || strings.HasPrefix(filePath, selector.path+"/"))) {
			matched = append(matched, index)
		}
	}
	return len(matched) > 0, matched
}

func unionPaths(left, right map[string]fileState) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for filePath := range left {
		set[filePath] = struct{}{}
	}
	for filePath := range right {
		set[filePath] = struct{}{}
	}
	paths := make([]string, 0, len(set))
	for filePath := range set {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	return paths
}

func sortedIndexPaths(entries map[string]indexEntry) []string {
	paths := make([]string, 0, len(entries))
	for filePath := range entries {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	return paths
}

func effectiveIndexFiles(base map[string]fileState, index stageIndex) map[string]fileState {
	result := cloneFileStates(base)
	for filePath, entry := range index.Entries {
		if entry.Kind == "deleted" {
			delete(result, filePath)
		} else {
			result[filePath] = fileState{Hash: entry.Object, Mode: entry.Mode}
		}
	}
	return result
}

func cloneFileStates(source map[string]fileState) map[string]fileState {
	result := make(map[string]fileState, len(source))
	for filePath, metadata := range source {
		result[filePath] = metadata
	}
	return result
}

func objectPath(root, hash string) string {
	return filepath.Join(root, ".gew", "objects", hash)
}

func storeObjectFromFile(root, filePath, expectedHash string) error {
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(filePath)))
	if err != nil {
		return err
	}
	actual := sha256.Sum256(content)
	actualHash := hex.EncodeToString(actual[:])
	if actualHash != expectedHash {
		return fmt.Errorf("file %s changed while staging; run 'gew add' again", filePath)
	}
	return storeObject(root, actualHash, content)
}

func storeObject(root, hash string, content []byte) error {
	directory := filepath.Join(root, ".gew", "objects")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	destination := objectPath(root, hash)
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(destination, content, 0o600)
}

func ensureBaselineObjects(root string, files map[string]fileState) error {
	for filePath, metadata := range files {
		if _, err := os.Stat(objectPath(root, metadata.Hash)); err == nil {
			continue
		}
		workspacePath := filepath.Join(root, filepath.FromSlash(filePath))
		currentHash, err := hashFile(workspacePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if currentHash == metadata.Hash {
			if err := storeObjectFromFile(root, filePath, metadata.Hash); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureSnapshotObjects(root string, state workspaceState) error {
	if err := ensureBaselineObjects(root, state.Files); err != nil {
		return err
	}
	missing := make([]string, 0)
	for filePath, metadata := range state.Files {
		if _, err := os.Stat(objectPath(root, metadata.Hash)); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, filePath)
		} else if err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	c, err := clientForWorkspace(state)
	if err != nil {
		return err
	}
	for _, filePath := range missing {
		metadata := state.Files[filePath]
		if metadata.BlobSHA == "" {
			return fmt.Errorf("content snapshot for %s is unavailable and has no remote blob SHA", filePath)
		}
		content, err := c.blob(state.Owner, state.Repository, metadata.BlobSHA)
		if err != nil {
			return fmt.Errorf("fetch baseline content for %s: %w", filePath, err)
		}
		hash := sha256.Sum256(content)
		actualHash := hex.EncodeToString(hash[:])
		if actualHash != metadata.Hash {
			return fmt.Errorf("remote baseline hash mismatch for %s", filePath)
		}
		if err := storeObject(root, actualHash, content); err != nil {
			return err
		}
	}
	return nil
}

func saveLocalCommit(root string, commit localCommit) error {
	directory := filepath.Join(root, ".gew", "commits")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(commit, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, commit.ID+".json"), append(data, '\n'), 0o600)
}

func loadLocalCommit(root, id string) (localCommit, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gew", "commits", id+".json"))
	if err != nil {
		return localCommit{}, err
	}
	var commit localCommit
	if err := json.Unmarshal(data, &commit); err != nil {
		return localCommit{}, err
	}
	if commit.ID != id {
		return localCommit{}, fmt.Errorf("local commit %s has an invalid ID", id)
	}
	return commit, nil
}

func localCommitID(commit localCommit) (string, error) {
	copyForHash := commit
	copyForHash.ID = ""
	copyForHash.RemoteSHA = ""
	copyForHash.PushedAt = nil
	copyForHash.SupersededBy = ""
	data, err := json.Marshal(copyForHash)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func firstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func writeFileMode(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode.Perm())
}

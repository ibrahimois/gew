package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (a app) diff(args []string) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	staged := flags.Bool("staged", false, "show staged changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gew diff [--staged]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if state.Backend == WorkspaceGit {
		return a.gitDiff(root, state, *staged)
	}
	if err := ensureSnapshotObjects(root, state); err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	indexFiles := effectiveIndexFiles(state.Files, index)
	var before, after map[string]fileState
	if *staged {
		before = state.Files
		after = indexFiles
	} else {
		before = indexFiles
		after, err = scanWorkspace(root)
		if err != nil {
			return err
		}
	}
	changes := changesBetween(before, after)
	for _, item := range changes {
		beforeContent, beforeOK, err := snapshotContent(root, item.Path, before)
		if err != nil {
			return err
		}
		var afterContent []byte
		var afterOK bool
		if *staged {
			afterContent, afterOK, err = snapshotContent(root, item.Path, after)
		} else if _, exists := after[item.Path]; exists {
			afterContent, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Path)))
			afterOK = err == nil
		}
		if err != nil {
			return err
		}
		printUnifiedDiff(a.out, item.Path, beforeContent, beforeOK, afterContent, afterOK)
	}
	return nil
}

func changesBetween(before, after map[string]fileState) []change {
	paths := unionPaths(before, after)
	changes := make([]change, 0)
	for _, filePath := range paths {
		old, oldExists := before[filePath]
		current, currentExists := after[filePath]
		switch {
		case !oldExists && currentExists:
			changes = append(changes, change{Kind: "created", Path: filePath})
		case oldExists && !currentExists:
			changes = append(changes, change{Kind: "deleted", Path: filePath})
		case oldExists && currentExists && old.Hash != current.Hash:
			changes = append(changes, change{Kind: "modified", Path: filePath})
		}
	}
	return changes
}

func snapshotContent(root, filePath string, files map[string]fileState) ([]byte, bool, error) {
	metadata, exists := files[filePath]
	if !exists {
		return nil, false, nil
	}
	content, err := os.ReadFile(objectPath(root, metadata.Hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("content snapshot for %s is unavailable; stage from a clean clone or pull", filePath)
	}
	return content, true, err
}

func printUnifiedDiff(output interface{ Write([]byte) (int, error) }, filePath string, before []byte, beforeExists bool, after []byte, afterExists bool) {
	fmt.Fprintf(output, "diff --gew a/%s b/%s\n", filePath, filePath)
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		fmt.Fprintln(output, "Binary files differ")
		return
	}
	oldLabel := "a/" + filePath
	newLabel := "b/" + filePath
	if !beforeExists {
		oldLabel = "/dev/null"
	}
	if !afterExists {
		newLabel = "/dev/null"
	}
	fmt.Fprintf(output, "--- %s\n+++ %s\n", oldLabel, newLabel)
	oldLines := splitDiffLines(string(before))
	newLines := splitDiffLines(string(after))
	operations := lineDiff(oldLines, newLines)
	for _, hunk := range diffHunks(operations, 3) {
		oldStart, newStart := 1, 1
		for _, operation := range operations[:hunk.start] {
			if operation.prefix != '+' {
				oldStart++
			}
			if operation.prefix != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, operation := range operations[hunk.start:hunk.end] {
			if operation.prefix != '+' {
				oldCount++
			}
			if operation.prefix != '-' {
				newCount++
			}
		}
		fmt.Fprintf(output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, operation := range operations[hunk.start:hunk.end] {
			fmt.Fprintf(output, "%c%s", operation.prefix, operation.line)
			if !strings.HasSuffix(operation.line, "\n") {
				fmt.Fprintln(output)
				fmt.Fprintln(output, "\\ No newline at end of file")
			}
		}
	}
}

type diffLine struct {
	prefix byte
	line   string
}

type diffHunk struct {
	start int
	end   int
}

func diffHunks(operations []diffLine, contextLines int) []diffHunk {
	hunks := make([]diffHunk, 0)
	for index, operation := range operations {
		if operation.prefix == ' ' {
			continue
		}
		start := index - contextLines
		if start < 0 {
			start = 0
		}
		end := index + contextLines + 1
		if end > len(operations) {
			end = len(operations)
		}
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].end {
			if end > hunks[len(hunks)-1].end {
				hunks[len(hunks)-1].end = end
			}
			continue
		}
		hunks = append(hunks, diffHunk{start: start, end: end})
	}
	return hunks
}

func splitDiffLines(content string) []string {
	if content == "" {
		return nil
	}
	parts := strings.SplitAfter(content, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func lineDiff(oldLines, newLines []string) []diffLine {
	if len(oldLines)*len(newLines) > 2_000_000 {
		operations := make([]diffLine, 0, len(oldLines)+len(newLines))
		for _, line := range oldLines {
			operations = append(operations, diffLine{prefix: '-', line: line})
		}
		for _, line := range newLines {
			operations = append(operations, diffLine{prefix: '+', line: line})
		}
		return operations
	}
	table := make([][]int, len(oldLines)+1)
	for index := range table {
		table[index] = make([]int, len(newLines)+1)
	}
	for oldIndex := len(oldLines) - 1; oldIndex >= 0; oldIndex-- {
		for newIndex := len(newLines) - 1; newIndex >= 0; newIndex-- {
			if oldLines[oldIndex] == newLines[newIndex] {
				table[oldIndex][newIndex] = table[oldIndex+1][newIndex+1] + 1
			} else if table[oldIndex+1][newIndex] >= table[oldIndex][newIndex+1] {
				table[oldIndex][newIndex] = table[oldIndex+1][newIndex]
			} else {
				table[oldIndex][newIndex] = table[oldIndex][newIndex+1]
			}
		}
	}
	operations := make([]diffLine, 0, len(oldLines)+len(newLines))
	oldIndex, newIndex := 0, 0
	for oldIndex < len(oldLines) && newIndex < len(newLines) {
		if oldLines[oldIndex] == newLines[newIndex] {
			operations = append(operations, diffLine{prefix: ' ', line: oldLines[oldIndex]})
			oldIndex++
			newIndex++
		} else if table[oldIndex+1][newIndex] >= table[oldIndex][newIndex+1] {
			operations = append(operations, diffLine{prefix: '-', line: oldLines[oldIndex]})
			oldIndex++
		} else {
			operations = append(operations, diffLine{prefix: '+', line: newLines[newIndex]})
			newIndex++
		}
	}
	for ; oldIndex < len(oldLines); oldIndex++ {
		operations = append(operations, diffLine{prefix: '-', line: oldLines[oldIndex]})
	}
	for ; newIndex < len(newLines); newIndex++ {
		operations = append(operations, diffLine{prefix: '+', line: newLines[newIndex]})
	}
	return operations
}

func sortedChanges(changes []change) []change {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

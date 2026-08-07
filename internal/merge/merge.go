// Package merge implements provider-independent three-way file merging.
package merge

import (
	"bytes"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"
)

// Content represents a file that may be absent on one side of a merge.
type Content struct {
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

type diffLine struct {
	prefix byte
	line   string
}

// File performs a three-way file merge and reports text and binary conflicts.
func File(base, ours, theirs Content) (Content, bool, bool) {
	if equal(ours, theirs) {
		return ours, false, false
	}
	if equal(ours, base) {
		return theirs, false, false
	}
	if equal(theirs, base) {
		return ours, false, false
	}
	binary := IsBinary(base.Content) || IsBinary(ours.Content) || IsBinary(theirs.Content)
	if binary {
		result := ours
		if !result.Exists {
			result = theirs
		}
		return result, true, true
	}
	merged, conflict := Text(base.Content, ours.Content, theirs.Content)
	mode := ours.Mode
	if mode == 0 {
		mode = theirs.Mode
	}
	return Content{Exists: true, Content: merged, Mode: mode}, conflict, false
}

// Text performs a line-oriented diff3 merge.
func Text(baseContent, oursContent, theirsContent []byte) ([]byte, bool) {
	baseLines := splitLines(string(baseContent))
	oursLines := splitLines(string(oursContent))
	theirsLines := splitLines(string(theirsContent))
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
		case slicesEqual(oursRegion, theirsRegion):
			result = append(result, oursRegion...)
		case slicesEqual(oursRegion, baseRegion):
			result = append(result, theirsRegion...)
		case slicesEqual(theirsRegion, baseRegion):
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

// IsBinary identifies content unsuitable for line-oriented merging.
func IsBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
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

func equal(left, right Content) bool {
	return left.Exists == right.Exists && (!left.Exists || bytes.Equal(left.Content, right.Content))
}

func slicesEqual(left, right []string) bool {
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

func splitLines(content string) []string {
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

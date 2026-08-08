package cli

import (
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

	"gew/internal/workspace"
)

const pullJournalVersion = 1

type pullJournal struct {
	Version      int                       `json:"version"`
	ID           string                    `json:"id"`
	TargetCommit string                    `json:"target_commit"`
	OldState     workspaceState            `json:"old_state"`
	Operations   []workspace.PullOperation `json:"operations"`
}

func pullJournalPath(root string) string { return filepath.Join(root, ".gew", "pull-journal.json") }

func (a app) deltaPull(ctx context.Context, root string, state workspaceState, remote Forge, commit string) error {
	finishPhase := a.sync.phase("tree")
	remoteFiles, err := remote.Tree(ctx, state.Remote, commit)
	finishPhase()
	if err != nil {
		return err
	}
	plan, err := workspace.BuildPullPlan(workspace.Manifest(state.Files), remoteFiles)
	if err != nil {
		return fmt.Errorf("plan incremental pull: %w", err)
	}
	if err := validatePullCollisions(root, state.Files, plan); err != nil {
		return err
	}
	stageRoot, err := makePullStage(root)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageRoot)
	stagedMetadata := make(map[string]fileState, len(plan.Downloads))
	if len(plan.Downloads) > 0 {
		finishPhase = a.sync.phase("download")
		artifacts, readErr := readBlobBatch(ctx, remote, state.Remote, plan.Downloads, remote.Capabilities().ReadConcurrency)
		finishPhase()
		if readErr != nil {
			return readErr
		}
		defer closeArtifacts(artifacts)
		paths := make([]string, 0, len(artifacts))
		for filePath := range artifacts {
			paths = append(paths, filePath)
		}
		sort.Strings(paths)
		for _, filePath := range paths {
			metadata, stageErr := stagePullArtifact(root, stageRoot, filePath, plan.Downloads[filePath], artifacts[filePath])
			if stageErr != nil {
				return stageErr
			}
			stagedMetadata[filePath] = metadata
		}
	}
	newFiles, err := pulledManifest(state.Files, remoteFiles, stagedMetadata)
	if err != nil {
		return err
	}
	newState := state
	newState.BaseCommit = commit
	newState.Files = newFiles
	finishPhase = a.sync.phase("apply")
	if err := applyPullPlan(root, stageRoot, state, newState, plan); err != nil {
		finishPhase()
		return errors.Join(err, recoverPull(root))
	}
	finishPhase()
	if a.sync != nil {
		var changedBytes int64
		for _, metadata := range stagedMetadata {
			changedBytes += metadata.Size
		}
		a.sync.add(int64(len(stagedMetadata)), changedBytes)
	}
	fmt.Fprintf(a.out, "Updated %s to %.12s.\n", state.Branch, commit)
	return nil
}

func makePullStage(root string) (string, error) {
	temporaryRoot := filepath.Join(root, ".gew", "tmp")
	if err := os.MkdirAll(temporaryRoot, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(temporaryRoot, "pull-")
}

func stagePullArtifact(root, stageRoot, filePath string, remote RemoteFile, artifact *SnapshotArtifact) (fileState, error) {
	target := filepath.Join(stageRoot, "files", filepath.FromSlash(filePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fileState{}, err
	}
	mode := os.FileMode(0o644)
	if remote.Mode&0o111 != 0 {
		mode = 0o755
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fileState{}, err
	}
	if _, err := artifact.Seek(0, io.SeekStart); err != nil {
		output.Close()
		return fileState{}, err
	}
	hasher := sha256.New()
	objectsDirectory := filepath.Join(root, ".gew", "objects")
	if err := os.MkdirAll(objectsDirectory, 0o700); err != nil {
		output.Close()
		return fileState{}, err
	}
	objectTemporary, err := os.CreateTemp(objectsDirectory, ".gew-object-")
	if err != nil {
		output.Close()
		return fileState{}, err
	}
	objectName := objectTemporary.Name()
	defer os.Remove(objectName)
	written, copyErr := io.Copy(io.MultiWriter(output, objectTemporary, hasher), io.LimitReader(artifact, maxRemoteSnapshot+1))
	closeErr := output.Close()
	closeErr = errors.Join(closeErr, objectTemporary.Close())
	if copyErr == nil && written > maxRemoteSnapshot {
		copyErr = fmt.Errorf("pulled blob %s exceeds %d bytes", filePath, maxRemoteSnapshot)
	}
	if copyErr == nil && remote.Size > 0 && written != remote.Size {
		copyErr = fmt.Errorf("pulled blob %s has %d bytes, expected %d", filePath, written, remote.Size)
	}
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fileState{}, err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	if err := publishObjectFile(root, objectName, hash); err != nil {
		return fileState{}, err
	}
	return fileState{BlobSHA: remote.BlobID, Hash: hash, Mode: uint32(mode), Size: written}, nil
}

func publishObjectFile(root, temporary, hash string) error {
	destination := objectPath(root, hash)
	if _, err := os.Stat(destination); err == nil {
		return os.Remove(temporary)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

func pulledManifest(old map[string]fileState, remote map[string]RemoteFile, changed map[string]fileState) (map[string]fileState, error) {
	result := make(map[string]fileState, len(remote))
	for filePath, remoteFile := range remote {
		if metadata, ok := changed[filePath]; ok {
			result[filePath] = metadata
			continue
		}
		metadata, ok := old[filePath]
		if !ok {
			return nil, fmt.Errorf("pull plan did not stage new path %s", filePath)
		}
		metadata.BlobSHA = remoteFile.BlobID
		if remoteFile.Mode&0o111 != 0 {
			metadata.Mode = 0o755
		} else {
			metadata.Mode = 0o644
		}
		if remoteFile.Size >= 0 {
			metadata.Size = remoteFile.Size
		}
		result[filePath] = metadata
	}
	return result, nil
}

func validatePullCollisions(root string, tracked map[string]fileState, plan workspace.PullPlan) error {
	for _, operation := range plan.Operations {
		target := filepath.Join(root, filepath.FromSlash(operation.Path))
		_, statErr := os.Lstat(target)
		_, wasTracked := tracked[operation.Path]
		if operation.Kind == workspace.PullCreate && !wasTracked && statErr == nil {
			return fmt.Errorf("remote path %s collides with an untracked local path", operation.Path)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

func applyPullPlan(root, stageRoot string, oldState, newState workspaceState, plan workspace.PullPlan) error {
	id := filepath.Base(stageRoot)
	journal := pullJournal{Version: pullJournalVersion, ID: id, TargetCommit: newState.BaseCommit, OldState: oldState, Operations: plan.Operations}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(pullJournalPath(root), append(data, '\n'), 0o600); err != nil {
		return err
	}
	recoveryRoot := filepath.Join(root, ".gew", "recovery", id)
	if err := os.MkdirAll(recoveryRoot, 0o700); err != nil {
		return err
	}
	for _, operation := range plan.Operations {
		target := filepath.Join(root, filepath.FromSlash(operation.Path))
		backup := filepath.Join(recoveryRoot, filepath.FromSlash(operation.Path))
		if _, tracked := oldState.Files[operation.Path]; tracked {
			if _, err := os.Lstat(target); err == nil {
				if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
					return err
				}
				if err := os.Rename(target, backup); err != nil {
					return err
				}
			}
		}
		switch operation.Kind {
		case workspace.PullDelete:
			continue
		case workspace.PullMode:
			if err := copyPulledFile(backup, target, operation.Remote.Mode); err != nil {
				return err
			}
		case workspace.PullCreate, workspace.PullModify:
			source := filepath.Join(stageRoot, "files", filepath.FromSlash(operation.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Rename(source, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported pull operation %q", operation.Kind)
		}
	}
	if err := saveState(root, newState); err != nil {
		return err
	}
	if err := os.Remove(pullJournalPath(root)); err != nil {
		return err
	}
	return os.RemoveAll(recoveryRoot)
}

func copyPulledFile(source, target string, remoteMode uint32) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if remoteMode&0o111 != 0 {
		mode = 0o755
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

func recoverPull(root string) error {
	data, err := os.ReadFile(pullJournalPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal pullJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("parse pull recovery journal: %w", err)
	}
	if journal.Version != pullJournalVersion || strings.TrimSpace(journal.ID) == "" || filepath.Base(journal.ID) != journal.ID {
		return errors.New("invalid pull recovery journal")
	}
	recoveryRoot := filepath.Join(root, ".gew", "recovery", journal.ID)
	for index := len(journal.Operations) - 1; index >= 0; index-- {
		operation := journal.Operations[index]
		target := filepath.Join(root, filepath.FromSlash(operation.Path))
		backup := filepath.Join(recoveryRoot, filepath.FromSlash(operation.Path))
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(backup); err == nil {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Rename(backup, target); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := saveState(root, journal.OldState); err != nil {
		return err
	}
	if err := os.Remove(pullJournalPath(root)); err != nil {
		return err
	}
	return os.RemoveAll(recoveryRoot)
}

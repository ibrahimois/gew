package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gew/internal/forge"
)

func proveAppliedCommit(ctx context.Context, remote Forge, inspector ForgeCommitInspector, request ApplyCommitRequest, result ApplyCommitResult, previous map[string]RemoteFile) (map[string]RemoteFile, error) {
	strategy := remote.Capabilities().PushProof
	if strategy == "" {
		strategy = forge.PushProofStrict
	}
	switch strategy {
	case forge.PushProofTree:
		if result.TargetHead != result.CommitID || result.TreeID == "" || result.ChangedFiles == nil {
			return nil, errors.New("provider returned incomplete mutation/tree proof")
		}
		return applyChangedEvidence(previous, request.Changes, result.ChangedFiles)
	case forge.PushProofChangedBytes:
		if result.TargetHead != result.CommitID {
			return nil, errors.New("provider mutation response did not confirm the target head")
		}
		details, err := inspector.CommitDetails(ctx, request.Repository, result.CommitID)
		if err != nil {
			return nil, fmt.Errorf("read exact commit proof: %w", err)
		}
		if !commitMatchesRequest(details, request, result.CommitID) {
			return nil, errors.New("provider commit details do not match the submitted mutation")
		}
		reader, hasRevisionReader := remote.(forge.ForgeRevisionBlobReader)
		updated := cloneRemoteFiles(previous)
		for _, change := range request.Changes {
			if change.Operation == "delete" {
				delete(updated, change.Path)
				continue
			}
			metadata, hasEvidence := result.ChangedFiles[change.Path]
			var content []byte
			var err error
			if hasEvidence && metadata.BlobID != "" {
				content, err = remote.Blob(ctx, request.Repository, metadata)
			} else if hasRevisionReader {
				content, metadata, err = reader.BlobAtRevision(ctx, request.Repository, result.CommitID, change.Path)
			} else {
				return nil, errors.New("provider declared changed-byte proof without exact changed-file evidence")
			}
			if err != nil {
				return nil, fmt.Errorf("read changed path %s at %.12s: %w", change.Path, result.CommitID, err)
			}
			if !bytes.Equal(content, change.Content) {
				return nil, fmt.Errorf("provider changed-byte proof differs at %s", change.Path)
			}
			if metadata.BlobID == "" {
				return nil, fmt.Errorf("provider changed-byte proof has no identity for %s", change.Path)
			}
			metadata.Mode = change.Mode
			metadata.Size = int64(len(content))
			updated[change.Path] = metadata
		}
		return updated, nil
	case forge.PushProofStrict:
		confirmed, err := remote.Head(ctx, request.Repository, targetBranch(request))
		if err != nil {
			return nil, fmt.Errorf("refresh provider head after mutation: %w", err)
		}
		if confirmed != result.CommitID {
			return nil, fmt.Errorf("provider did not confirm commit %.12s on target branch", result.CommitID)
		}
		files, err := remote.Tree(ctx, request.Repository, result.CommitID)
		if err != nil {
			return nil, fmt.Errorf("read strict commit tree: %w", err)
		}
		for _, change := range request.Changes {
			metadata, exists := files[change.Path]
			if change.Operation == "delete" {
				if exists {
					return nil, fmt.Errorf("strict proof retained deleted path %s", change.Path)
				}
				continue
			}
			if !exists {
				return nil, fmt.Errorf("strict proof is missing changed path %s", change.Path)
			}
			content, err := remote.Blob(ctx, request.Repository, metadata)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(content, change.Content) {
				return nil, fmt.Errorf("strict proof differs at %s", change.Path)
			}
		}
		return files, nil
	default:
		return nil, fmt.Errorf("unsupported provider push proof strategy %q", strategy)
	}
}

func commitMatchesRequest(details RemoteCommit, request ApplyCommitRequest, commitID string) bool {
	if details.ID != "" && details.ID != commitID {
		return false
	}
	if strings.TrimSpace(details.Message) != strings.TrimSpace(request.Message) {
		return false
	}
	if request.ExpectedHead == "" {
		if len(details.ParentIDs) != 0 {
			return false
		}
	} else if len(details.ParentIDs) == 0 || details.ParentIDs[0] != request.ExpectedHead {
		return false
	}
	expected := make([]string, 0, len(request.Changes))
	for _, change := range request.Changes {
		expected = append(expected, change.Path)
	}
	actual := append([]string(nil), details.Paths...)
	sort.Strings(expected)
	sort.Strings(actual)
	return strings.Join(expected, "\x00") == strings.Join(actual, "\x00")
}

func applyChangedEvidence(previous map[string]RemoteFile, changes []RemoteChange, evidence map[string]RemoteFile) (map[string]RemoteFile, error) {
	updated := cloneRemoteFiles(previous)
	for _, change := range changes {
		metadata, exists := evidence[change.Path]
		if !exists {
			return nil, fmt.Errorf("provider proof omitted %s", change.Path)
		}
		if change.Operation == "delete" {
			delete(updated, change.Path)
			continue
		}
		if metadata.BlobID == "" {
			return nil, fmt.Errorf("provider proof omitted blob identity for %s", change.Path)
		}
		metadata.Mode = change.Mode
		metadata.Size = int64(len(change.Content))
		updated[change.Path] = metadata
	}
	return updated, nil
}

func cloneRemoteFiles(source map[string]RemoteFile) map[string]RemoteFile {
	result := make(map[string]RemoteFile, len(source))
	for filePath, metadata := range source {
		result[filePath] = metadata
	}
	return result
}

func targetBranch(request ApplyCommitRequest) string {
	if request.NewBranch != "" {
		return request.NewBranch
	}
	return request.Branch
}

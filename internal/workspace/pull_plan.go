package workspace

import (
	"fmt"
	"sort"

	"gew/internal/forge"
)

type PullOperationKind string

const (
	PullCreate PullOperationKind = "create"
	PullModify PullOperationKind = "modify"
	PullDelete PullOperationKind = "delete"
	PullMode   PullOperationKind = "mode"
)

type PullOperation struct {
	Kind   PullOperationKind
	Path   string
	Remote forge.RemoteFile
}

type PullPlan struct {
	Operations []PullOperation
	Downloads  map[string]forge.RemoteFile
}

func BuildPullPlan(base Manifest, remote map[string]forge.RemoteFile) (PullPlan, error) {
	plan := PullPlan{Downloads: make(map[string]forge.RemoteFile)}
	for filePath, metadata := range remote {
		cleaned, err := forge.ValidateRemotePath(filePath)
		if err != nil || cleaned != filePath {
			if err == nil {
				err = fmt.Errorf("remote manifest contains non-canonical path %q", filePath)
			}
			return PullPlan{}, err
		}
		old, exists := base[filePath]
		if !exists {
			plan.Operations = append(plan.Operations, PullOperation{Kind: PullCreate, Path: filePath, Remote: metadata})
			plan.Downloads[filePath] = metadata
			continue
		}
		if old.BlobSHA == "" || metadata.BlobID == "" || old.BlobSHA != metadata.BlobID {
			plan.Operations = append(plan.Operations, PullOperation{Kind: PullModify, Path: filePath, Remote: metadata})
			plan.Downloads[filePath] = metadata
			continue
		}
		if normalizedMode(old.Mode) != normalizedMode(metadata.Mode) {
			plan.Operations = append(plan.Operations, PullOperation{Kind: PullMode, Path: filePath, Remote: metadata})
		}
	}
	for filePath := range base {
		if _, exists := remote[filePath]; !exists {
			if _, err := forge.ValidateRemotePath(filePath); err != nil {
				return PullPlan{}, err
			}
			plan.Operations = append(plan.Operations, PullOperation{Kind: PullDelete, Path: filePath})
		}
	}
	sort.Slice(plan.Operations, func(i, j int) bool {
		if plan.Operations[i].Path == plan.Operations[j].Path {
			return plan.Operations[i].Kind < plan.Operations[j].Kind
		}
		return plan.Operations[i].Path < plan.Operations[j].Path
	})
	return plan, nil
}

func normalizedMode(mode uint32) uint32 {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

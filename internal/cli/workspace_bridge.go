package cli

import "gew/internal/workspace"

type WorkspaceBackendKind = workspace.BackendKind
type hybridState = workspace.HybridState
type preparedGitExport = workspace.PreparedGitExport
type gitExportReceipt = workspace.GitExportReceipt

const (
	WorkspaceGew = workspace.Gew
	WorkspaceGit = workspace.Git
)

func normalizeWorkspaceBackend(kind WorkspaceBackendKind) (WorkspaceBackendKind, error) {
	return workspace.NormalizeBackend(kind)
}

func gewTrackingRef(provider ForgeKind, branch string) (string, error) {
	return workspace.TrackingRef(provider, branch)
}

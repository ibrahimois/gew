package cli

import (
	"context"
	"io"

	"gew/internal/forge"
	"gew/internal/forge/registry"
)

type profile = forge.Config
type Forge = forge.Forge
type ForgeKind = forge.ForgeKind
type AuthKind = forge.AuthKind
type RepositoryRef = forge.RepositoryRef
type RepositoryInfo = forge.RepositoryInfo
type RemoteFile = forge.RemoteFile
type RemoteChange = forge.RemoteChange
type RemoteCommit = forge.RemoteCommit
type ApplyCommitRequest = forge.ApplyCommitRequest
type ApplyCommitResult = forge.ApplyCommitResult
type ForgeCapabilities = forge.ForgeCapabilities
type ForgeCommitInspector = forge.ForgeCommitInspector
type ForgeCommitWriter = forge.ForgeCommitWriter
type ForgeReleasePublisher = forge.ForgeReleasePublisher
type RemoteRelease = forge.RemoteRelease
type RemoteReleaseAsset = forge.RemoteReleaseAsset
type CreateReleaseRequest = forge.CreateReleaseRequest
type ForgeSnapshotResult = forge.SnapshotResult
type SnapshotArtifact = forge.SnapshotArtifact
type SnapshotSource = forge.SnapshotSource

const (
	ForgeGitea           = forge.ForgeGitea
	ForgeGitHub          = forge.ForgeGitHub
	ForgeGitLab          = forge.ForgeGitLab
	ForgeBitbucket       = forge.ForgeBitbucket
	ForgeAzure           = forge.ForgeAzure
	AuthToken            = forge.AuthToken
	maxRemoteSnapshot    = forge.MaxRemoteSnapshot
	SnapshotSourceNative = forge.SnapshotSourceNative
)

var (
	ErrNotFound    = forge.ErrNotFound
	ErrStaleHead   = forge.ErrStaleHead
	ErrUnsupported = forge.ErrUnsupported
)

func registeredForgeKinds() []ForgeKind                  { return registry.Kinds() }
func normalizeForgeKind(value string) (ForgeKind, error) { return registry.NormalizeKind(value) }
func defaultAuthKind(kind ForgeKind) AuthKind            { return registry.DefaultAuthKind(kind) }
func forgeFromProfile(p profile) (Forge, error)          { return registry.FromConfig(p) }
func forgeWriter(remote Forge, newBranch bool) (ForgeCommitWriter, error) {
	return forge.Writer(remote, newBranch)
}
func forgeSnapshot(ctx context.Context, remote Forge, ref RepositoryRef, revision string) (ForgeSnapshotResult, error) {
	return forge.SnapshotWithTree(ctx, remote, ref, revision)
}
func forgeReleasePublisher(remote Forge) (ForgeReleasePublisher, error) {
	return forge.ReleasePublisher(remote)
}
func isRemoteNotFound(err error) bool                 { return forge.IsRemoteNotFound(err) }
func validateRemotePath(value string) (string, error) { return forge.ValidateRemotePath(value) }
func normalizeServerURL(value string) (string, error) { return forge.NormalizeServerURL(value) }
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	return forge.ReadBounded(reader, limit)
}
func ArtifactFromBytes(data []byte, source SnapshotSource) (*SnapshotArtifact, error) {
	return forge.ArtifactFromBytes(data, source)
}
func readBlobBatch(ctx context.Context, remote Forge, ref RepositoryRef, files map[string]RemoteFile, concurrency int) (map[string]*SnapshotArtifact, error) {
	return forge.ReadBlobBatch(ctx, remote, ref, files, concurrency)
}
func closeArtifacts(artifacts map[string]*SnapshotArtifact) error {
	return forge.CloseArtifacts(artifacts)
}

package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

type ForgeKind string

const (
	ForgeGitea     ForgeKind = "gitea"
	ForgeGitHub    ForgeKind = "github"
	ForgeGitLab    ForgeKind = "gitlab"
	ForgeBitbucket ForgeKind = "bitbucket"
	ForgeAzure     ForgeKind = "azure"
)

type AuthKind string

const (
	AuthToken   AuthKind = "token"
	AuthBearer  AuthKind = "bearer"
	AuthBasic   AuthKind = "basic"
	AuthPrivate AuthKind = "private-token"
	AuthPAT     AuthKind = "pat"
)

type RepositoryRef struct {
	Forge     ForgeKind `json:"forge"`
	Server    string    `json:"server"`
	Namespace string    `json:"namespace,omitempty"`
	Project   string    `json:"project,omitempty"`
	Name      string    `json:"name"`
	RemoteID  string    `json:"remote_id,omitempty"`
	Canonical string    `json:"canonical,omitempty"`
}

func (r RepositoryRef) DisplayName() string {
	if r.Canonical != "" {
		return r.Canonical
	}
	parts := make([]string, 0, 3)
	if r.Namespace != "" {
		parts = append(parts, r.Namespace)
	}
	if r.Project != "" && r.Project != r.Namespace {
		parts = append(parts, r.Project)
	}
	if r.Name != "" {
		parts = append(parts, r.Name)
	}
	return strings.Join(parts, "/")
}

type RepositoryInfo struct {
	DefaultBranch string
	Empty         bool
}

type RemoteFile struct {
	BlobID       string
	Mode         uint32
	Size         int64
	LastCommitID string
}

type RemoteChange struct {
	Operation    string
	Path         string
	Content      []byte
	BlobID       string
	LastCommitID string
	Mode         uint32
}

type ApplyCommitRequest struct {
	Repository   RepositoryRef
	Branch       string
	NewBranch    string
	ExpectedHead string
	Message      string
	Changes      []RemoteChange
}

type ApplyCommitResult struct {
	CommitID       string
	ParentIDs      []string
	ConditionalRef bool
}

type ForgeCapabilities struct {
	ArchiveSnapshot bool
	AtomicMultiFile bool
	ConditionalRef  bool
	BranchCreate    bool
	Push            bool
}

type RemoteCommit struct {
	ID        string
	Message   string
	ParentIDs []string
	Paths     []string
}

type Forge interface {
	Kind() ForgeKind
	Capabilities() ForgeCapabilities
	Probe(context.Context) error
	ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error)
	Head(context.Context, RepositoryRef, string) (string, error)
	Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error)
	Blob(context.Context, RepositoryRef, RemoteFile) ([]byte, error)
	Snapshot(context.Context, RepositoryRef, string) ([]byte, error)
	CommitDetails(context.Context, RepositoryRef, string) (RemoteCommit, error)
	ApplyCommit(context.Context, ApplyCommitRequest) (ApplyCommitResult, error)
}

var (
	ErrNotFound    = errors.New("remote resource not found")
	ErrStaleHead   = errors.New("remote branch head changed")
	ErrUnsupported = errors.New("provider capability is not supported")
)

type RemoteError struct {
	Kind   ForgeKind
	Status int
	Method string
	URL    string
	Body   string
}

func (e *RemoteError) Error() string {
	message := strings.TrimSpace(e.Body)
	if message == "" {
		message = "request failed"
	}
	return fmt.Sprintf("%s remote API %s %s returned %d: %s", e.Kind, e.Method, e.URL, e.Status, message)
}

func (e *RemoteError) Unwrap() error {
	if e.Status == 404 {
		return ErrNotFound
	}
	if e.Status == 409 || e.Status == 412 || e.Status == 422 {
		return ErrStaleHead
	}
	return nil
}

func isRemoteNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func validateRemotePath(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("invalid repository path %q", value)
	}
	cleaned := path.Clean(strings.TrimPrefix(value, "/"))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return "", fmt.Errorf("unsafe repository path %q", value)
	}
	return cleaned, nil
}

// validateApplyResult enforces the push parent invariant. ConditionalRef means
// the provider atomically refused an unexpected branch head. A provider without
// that guarantee must return the created commit's parents so the engine can
// distinguish a clean append from a concurrent append.
func validateApplyResult(request ApplyCommitRequest, result ApplyCommitResult) error {
	if strings.TrimSpace(result.CommitID) == "" {
		return errors.New("provider returned an empty commit ID")
	}
	if result.ConditionalRef {
		return nil
	}
	if request.ExpectedHead == "" {
		if len(result.ParentIDs) != 0 {
			return fmt.Errorf("remote commit %s unexpectedly has a parent; synchronize before pushing again", result.CommitID)
		}
		return nil
	}
	if len(result.ParentIDs) == 0 || result.ParentIDs[0] != request.ExpectedHead {
		return fmt.Errorf("remote commit %s was appended to unexpected parent; synchronize before pushing again", result.CommitID)
	}
	return nil
}

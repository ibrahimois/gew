package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func NormalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid provider URL %q; include http:// or https://", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("provider URL must use http or https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

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

// Config contains the provider settings shared by the CLI and forge adapters.
type Config struct {
	Provider ForgeKind `json:"provider,omitempty"`
	URL      string    `json:"url"`
	Token    string    `json:"token"`
	AuthKind AuthKind  `json:"auth_kind,omitempty"`
	Username string    `json:"username,omitempty"`
	Insecure bool      `json:"insecure,omitempty"`
}

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
	BranchCreate bool
	Push         bool
}

type RemoteCommit struct {
	ID        string
	Message   string
	ParentIDs []string
	Paths     []string
}

type RepositoryReader interface {
	Head(context.Context, RepositoryRef, string) (string, error)
	Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error)
	Blob(context.Context, RepositoryRef, RemoteFile) ([]byte, error)
}

type Forge interface {
	RepositoryReader
	Kind() ForgeKind
	Capabilities() ForgeCapabilities
	Probe(context.Context) error
	ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error)
}

type ForgeSnapshotter interface {
	Snapshot(context.Context, RepositoryRef, string) ([]byte, error)
}

type ForgeCommitInspector interface {
	CommitDetails(context.Context, RepositoryRef, string) (RemoteCommit, error)
}

type ForgeCommitWriter interface {
	ForgeCommitInspector
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
	return nil
}

func remoteErrorStatus(err error) (int, bool) {
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) {
		return 0, false
	}
	return remoteErr.Status, true
}

func RemoteErrorHasStatus(err error, candidates ...int) bool {
	status, ok := remoteErrorStatus(err)
	if !ok {
		return false
	}
	for _, candidate := range candidates {
		if status == candidate {
			return true
		}
	}
	return false
}

// confirmStaleHead classifies a provider mutation error as a concurrency
// failure only when an exact branch read proves that the expected head moved.
// The original sanitized provider error remains in the returned error chain.
func ConfirmStaleHead(ctx context.Context, remote RepositoryReader, ref RepositoryRef, branch, expectedHead string, mutationErr error) error {
	if mutationErr == nil || expectedHead == "" {
		return mutationErr
	}
	observed, err := remote.Head(ctx, ref, branch)
	if err != nil || observed == expectedHead {
		return mutationErr
	}
	return errors.Join(mutationErr, ErrStaleHead)
}

func IsRemoteNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func ValidateRemotePath(value string) (string, error) {
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

func validateApplyRequest(kind ForgeKind, request ApplyCommitRequest) (ApplyCommitRequest, error) {
	request.Branch = strings.TrimSpace(request.Branch)
	request.NewBranch = strings.TrimSpace(request.NewBranch)
	request.Message = strings.TrimSpace(request.Message)
	if request.Branch == "" {
		return ApplyCommitRequest{}, errors.New("remote commit branch is empty")
	}
	if request.Message == "" {
		return ApplyCommitRequest{}, errors.New("remote commit message is empty")
	}
	if len(request.Changes) == 0 {
		return ApplyCommitRequest{}, errors.New("remote commit has no changes")
	}
	if request.Repository.Forge != "" && request.Repository.Forge != kind {
		return ApplyCommitRequest{}, fmt.Errorf("repository provider %q does not match selected provider %q", request.Repository.Forge, kind)
	}
	request.Changes = append([]RemoteChange(nil), request.Changes...)
	seen := make(map[string]struct{}, len(request.Changes))
	for index := range request.Changes {
		change := request.Changes[index]
		switch change.Operation {
		case "create", "update", "delete":
		default:
			return ApplyCommitRequest{}, fmt.Errorf("unsupported remote change operation %q", change.Operation)
		}
		cleaned, err := ValidateRemotePath(change.Path)
		if err != nil {
			return ApplyCommitRequest{}, err
		}
		if _, exists := seen[cleaned]; exists {
			return ApplyCommitRequest{}, fmt.Errorf("duplicate repository path %q", cleaned)
		}
		seen[cleaned] = struct{}{}
		change.Path = cleaned
		change.Content = append([]byte(nil), change.Content...)
		request.Changes[index] = change
	}
	return request, nil
}

type validatedForgeWriter struct {
	kind ForgeKind
	raw  ForgeCommitWriter
}

func (w validatedForgeWriter) CommitDetails(ctx context.Context, ref RepositoryRef, commit string) (RemoteCommit, error) {
	return w.raw.CommitDetails(ctx, ref, commit)
}

func (w validatedForgeWriter) ApplyCommit(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	validated, err := validateApplyRequest(w.kind, request)
	if err != nil {
		return ApplyCommitResult{}, err
	}
	result, err := w.raw.ApplyCommit(ctx, validated)
	if err != nil {
		return ApplyCommitResult{}, err
	}
	if err := validateApplyResult(validated, result); err != nil {
		return ApplyCommitResult{}, err
	}
	return result, nil
}

func Writer(remote Forge, newBranch bool) (ForgeCommitWriter, error) {
	capabilities := remote.Capabilities()
	if !capabilities.Push {
		return nil, fmt.Errorf("%s push is disabled because its concurrency safety has not been verified: %w", remote.Kind(), ErrUnsupported)
	}
	if newBranch && !capabilities.BranchCreate {
		return nil, fmt.Errorf("%s does not support creating branches through gew: %w", remote.Kind(), ErrUnsupported)
	}
	raw, ok := remote.(ForgeCommitWriter)
	if !ok {
		return nil, fmt.Errorf("%s advertises push without implementing the writer contract: %w", remote.Kind(), ErrUnsupported)
	}
	return validatedForgeWriter{kind: remote.Kind(), raw: raw}, nil
}

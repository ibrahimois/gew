package gitea

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"gew/internal/forge"
)

type giteaRepository struct {
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
}

type giteaBranchResponse struct {
	Name   string `json:"name"`
	Commit struct {
		ID  string `json:"id"`
		SHA string `json:"sha"`
	} `json:"commit"`
}

func (b giteaBranchResponse) commitSHA() string {
	if b.Commit.ID != "" {
		return b.Commit.ID
	}
	return b.Commit.SHA
}

type giteaTreeEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
}

type giteaTreeResponse struct {
	Tree       []giteaTreeEntry `json:"tree"`
	Truncated  bool             `json:"truncated"`
	Page       int              `json:"page"`
	TotalCount int              `json:"total_count"`
}

type giteaBlobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
}

type giteaCommitDetails struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Files []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}

type giteaChangeOperation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	SHA       string `json:"sha,omitempty"`
}

type giteaChangeFilesRequest struct {
	Branch    string                 `json:"branch"`
	NewBranch string                 `json:"new_branch,omitempty"`
	Message   string                 `json:"message"`
	Files     []giteaChangeOperation `json:"files"`
}

type giteaFilesResponse struct {
	Commit struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	} `json:"commit"`
}

type giteaForge struct {
	baseURL   string
	requester *forge.HTTPRequester
}

var (
	_ forge.Forge                   = (*giteaForge)(nil)
	_ forge.ForgeSnapshotter        = (*giteaForge)(nil)
	_ forge.ForgeCommitWriter       = (*giteaForge)(nil)
	_ forge.ForgeRevisionBlobReader = (*giteaForge)(nil)
	_ forge.ForgeReleasePublisher   = (*giteaForge)(nil)
)

type giteaRelease struct {
	ID              int64  `json:"id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	URL             string `json:"html_url"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

type giteaReleaseAsset struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	DownloadCount int64  `json:"download_count"`
	BrowserURL    string `json:"browser_download_url"`
}

func New(p forge.Config) (*giteaForge, error) {
	server, err := forge.NormalizeServerURL(p.URL)
	if err != nil {
		return nil, err
	}
	p.Provider = forge.ForgeGitea
	p.HTTP1Only = true
	if p.AuthKind != forge.AuthToken && p.AuthKind != forge.AuthBearer {
		return nil, fmt.Errorf("gitea does not support authentication kind %q", p.AuthKind)
	}
	auth := func(request *http.Request) {
		prefix := "token "
		if p.AuthKind == forge.AuthBearer {
			prefix = "Bearer "
		}
		request.Header.Set("Authorization", prefix+p.Token)
	}
	requester, err := forge.NewHTTPRequester(p, server, auth, make(http.Header))
	if err != nil {
		return nil, err
	}
	return &giteaForge{
		baseURL:   server,
		requester: requester,
	}, nil
}

func (g *giteaForge) Kind() forge.ForgeKind { return forge.ForgeGitea }

func (g *giteaForge) Capabilities() forge.ForgeCapabilities {
	return forge.ForgeCapabilities{BranchCreate: true, Push: true, NativeSnapshot: true, RecursiveTree: true, ReadConcurrency: 4, PushProof: forge.PushProofChangedBytes}
}

func (g *giteaForge) Probe(ctx context.Context) error {
	var version struct {
		Version string `json:"version"`
	}
	return g.requester.DoJSON(ctx, http.MethodGet, "/api/v1/version", nil, &version)
}

func (g *giteaForge) ResolveRepository(ctx context.Context, value string) (forge.RepositoryRef, forge.RepositoryInfo, error) {
	owner, name, err := parseGiteaRepository(value)
	if err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	ref := forge.RepositoryRef{Forge: forge.ForgeGitea, Server: g.baseURL, Namespace: owner, Name: name, Canonical: owner + "/" + name}
	var response giteaRepository
	if err := g.requester.DoJSON(ctx, http.MethodGet, giteaRepoAPIPath(ref), nil, &response); err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	return ref, forge.RepositoryInfo{DefaultBranch: response.DefaultBranch, Empty: response.Empty}, nil
}

func (g *giteaForge) Head(ctx context.Context, ref forge.RepositoryRef, branch string) (string, error) {
	var response giteaBranchResponse
	endpoint := giteaRepoAPIPath(ref) + "/branches/" + url.PathEscape(branch)
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	commit := response.commitSHA()
	if commit == "" {
		return "", fmt.Errorf("gitea returned no commit ID for branch %q", branch)
	}
	return commit, nil
}

func (g *giteaForge) Tree(ctx context.Context, ref forge.RepositoryRef, commit string) (map[string]forge.RemoteFile, error) {
	result := make(map[string]forge.RemoteFile)
	for pageNumber := 1; ; pageNumber++ {
		endpoint := fmt.Sprintf("%s/git/trees/%s?recursive=true&page=%d&per_page=1000", giteaRepoAPIPath(ref), url.PathEscape(commit), pageNumber)
		var response giteaTreeResponse
		if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return nil, err
		}
		for _, entry := range response.Tree {
			if entry.Type != "blob" {
				continue
			}
			cleaned, err := forge.ValidateRemotePath(entry.Path)
			if err != nil {
				return nil, err
			}
			mode := uint32(0)
			if entry.Mode != "" {
				parsed, parseErr := strconv.ParseUint(entry.Mode, 8, 32)
				if parseErr == nil {
					mode = uint32(parsed)
				}
			}
			result[cleaned] = forge.RemoteFile{BlobID: entry.SHA, Mode: mode, Size: entry.Size}
		}
		if !response.Truncated || len(response.Tree) == 0 {
			break
		}
	}
	return result, nil
}

func (g *giteaForge) Blob(ctx context.Context, ref forge.RepositoryRef, file forge.RemoteFile) ([]byte, error) {
	var response giteaBlobResponse
	endpoint := giteaRepoAPIPath(ref) + "/git/blobs/" + url.PathEscape(file.BlobID)
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	return decodeGiteaBlob(response, file.BlobID)
}

func (g *giteaForge) BlobAtRevision(ctx context.Context, ref forge.RepositoryRef, revision, filePath string) ([]byte, forge.RemoteFile, error) {
	cleaned, err := forge.ValidateRemotePath(filePath)
	if err != nil {
		return nil, forge.RemoteFile{}, err
	}
	parts := strings.Split(cleaned, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	endpoint := giteaRepoAPIPath(ref) + "/contents/" + strings.Join(parts, "/") + "?ref=" + url.QueryEscape(revision)
	var response giteaBlobResponse
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, forge.RemoteFile{}, err
	}
	content, err := decodeGiteaBlob(response, response.SHA)
	return content, forge.RemoteFile{BlobID: response.SHA, Mode: 0o100644, Size: int64(len(content))}, err
}

func decodeGiteaBlob(response giteaBlobResponse, identity string) ([]byte, error) {
	if response.Encoding != "" && response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported blob encoding %q", response.Encoding)
	}
	content := strings.ReplaceAll(response.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decode blob %s: %w", identity, err)
	}
	return decoded, nil
}

func (g *giteaForge) Snapshot(ctx context.Context, ref forge.RepositoryRef, revision string) (*forge.SnapshotArtifact, error) {
	return g.requester.DownloadArtifact(ctx, giteaArchiveAPIPath(ref, revision), forge.SnapshotSourceNative)
}

func (g *giteaForge) CommitDetails(ctx context.Context, ref forge.RepositoryRef, commit string) (forge.RemoteCommit, error) {
	var response giteaCommitDetails
	endpoint := giteaRepoAPIPath(ref) + "/git/commits/" + url.PathEscape(commit) + "?stat=false&verification=false&files=true"
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return forge.RemoteCommit{}, err
	}
	result := forge.RemoteCommit{ID: response.SHA, Message: response.Commit.Message}
	if result.ID == "" {
		result.ID = commit
	}
	for _, parent := range response.Parents {
		result.ParentIDs = append(result.ParentIDs, parent.SHA)
	}
	for _, file := range response.Files {
		result.Paths = append(result.Paths, path.Clean(file.Filename))
	}
	return result, nil
}

func (g *giteaForge) ApplyCommit(ctx context.Context, request forge.ApplyCommitRequest) (forge.ApplyCommitResult, error) {
	if !g.Capabilities().Push {
		return forge.ApplyCommitResult{}, forge.ErrUnsupported
	}
	operations := make([]giteaChangeOperation, 0, len(request.Changes))
	var rawBytes int64
	for _, change := range request.Changes {
		rawBytes += int64(len(change.Content))
		operation := giteaChangeOperation{Operation: change.Operation, Path: change.Path, SHA: change.BlobID}
		if change.Operation != "delete" {
			operation.Content = base64.StdEncoding.EncodeToString(change.Content)
		}
		operations = append(operations, operation)
	}
	payload := giteaChangeFilesRequest{Branch: request.Branch, NewBranch: request.NewBranch, Message: request.Message, Files: operations}
	var response giteaFilesResponse
	temporary, err := os.CreateTemp("", "gew-gitea-commit-*")
	if err != nil {
		return forge.ApplyCommitResult{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	defer temporary.Close()
	if err := json.NewEncoder(temporary).Encode(payload); err != nil {
		return forge.ApplyCommitResult{}, err
	}
	stat, err := temporary.Stat()
	if err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if err := g.requester.DoBody(ctx, http.MethodPost, giteaRepoAPIPath(request.Repository)+"/contents", "application/json", stat.Size(), temporary, &response); err != nil {
		var remoteErr *forge.RemoteError
		if errors.As(err, &remoteErr) && (remoteErr.Status == http.StatusNotFound || remoteErr.Status == http.StatusMethodNotAllowed) {
			return forge.ApplyCommitResult{}, fmt.Errorf("%w; this gitea version may not support atomic multi-file changes", err)
		}
		if forge.RemoteErrorHasStatus(err, http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity) {
			err = forge.ConfirmStaleHead(ctx, g, request.Repository, request.Branch, request.ExpectedHead, err)
		}
		if errors.Is(err, forge.ErrRequestTooLarge) || rawBytes >= 8<<20 {
			encodedBytes := ((rawBytes + 2) / 3) * 4
			return forge.ApplyCommitResult{}, fmt.Errorf("gitea atomic commit payload has %d change(s), %d raw bytes, and about %d base64 bytes: %w", len(request.Changes), rawBytes, encodedBytes, err)
		}
		return forge.ApplyCommitResult{}, err
	}
	if response.Commit.SHA == "" {
		return forge.ApplyCommitResult{}, errors.New("gitea returned an empty commit ID")
	}
	parents := make([]string, 0, len(response.Commit.Parents))
	for _, parent := range response.Commit.Parents {
		parents = append(parents, parent.SHA)
	}
	changed := make(map[string]forge.RemoteFile, len(request.Changes))
	for _, change := range request.Changes {
		metadata := forge.RemoteFile{Mode: change.Mode}
		if change.Operation != "delete" {
			metadata.BlobID = giteaGitBlobID(change.Content)
			metadata.Size = int64(len(change.Content))
		}
		changed[change.Path] = metadata
	}
	return forge.ApplyCommitResult{CommitID: response.Commit.SHA, ParentIDs: parents, ConditionalRef: false, TargetHead: response.Commit.SHA, ChangedFiles: changed}, nil
}

func giteaGitBlobID(content []byte) string {
	hasher := sha1.New() //nolint:gosec // Git object identity, not a security digest.
	fmt.Fprintf(hasher, "blob %d%c", len(content), 0)
	hasher.Write(content)
	return hex.EncodeToString(hasher.Sum(nil))
}

func (g *giteaForge) FindReleaseByTag(ctx context.Context, ref forge.RepositoryRef, tag string) (forge.RemoteRelease, error) {
	var response giteaRelease
	endpoint := giteaRepoAPIPath(ref) + "/releases/tags/" + url.PathEscape(tag)
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return forge.RemoteRelease{}, err
	}
	return remoteGiteaRelease(response), nil
}

func (g *giteaForge) CreateRelease(ctx context.Context, request forge.CreateReleaseRequest) (forge.RemoteRelease, error) {
	payload := struct {
		TagName         string `json:"tag_name"`
		TargetCommitish string `json:"target_commitish"`
		Name            string `json:"name"`
		Body            string `json:"body"`
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
	}{request.TagName, request.TargetCommit, request.Title, request.Notes, request.Draft, request.Prerelease}
	var response giteaRelease
	if err := g.requester.DoJSON(ctx, http.MethodPost, giteaRepoAPIPath(request.Repository)+"/releases", payload, &response); err != nil {
		return forge.RemoteRelease{}, err
	}
	return remoteGiteaRelease(response), nil
}

func (g *giteaForge) ListReleaseAssets(ctx context.Context, ref forge.RepositoryRef, releaseID string) ([]forge.RemoteReleaseAsset, error) {
	var response []giteaReleaseAsset
	endpoint := giteaRepoAPIPath(ref) + "/releases/" + url.PathEscape(releaseID) + "/assets"
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	assets := make([]forge.RemoteReleaseAsset, 0, len(response))
	for _, asset := range response {
		assets = append(assets, remoteGiteaAsset(asset))
	}
	return assets, nil
}

func (g *giteaForge) UploadReleaseAsset(ctx context.Context, ref forge.RepositoryRef, releaseID, name string, size int64, content io.Reader) (forge.RemoteReleaseAsset, error) {
	temporary, err := os.CreateTemp("", "gew-release-asset-*")
	if err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	defer temporary.Close()

	writer := multipart.NewWriter(temporary)
	part, err := writer.CreateFormFile("attachment", name)
	if err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	written, err := io.Copy(part, io.LimitReader(content, size+1))
	if err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	if written != size {
		return forge.RemoteReleaseAsset{}, fmt.Errorf("asset %q supplied %d bytes, want %d", name, written, size)
	}
	if err := writer.Close(); err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	stat, err := temporary.Stat()
	if err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	endpoint := giteaRepoAPIPath(ref) + "/releases/" + url.PathEscape(releaseID) + "/assets?name=" + url.QueryEscape(name)
	var response giteaReleaseAsset
	if err := g.requester.DoBody(ctx, http.MethodPost, endpoint, writer.FormDataContentType(), stat.Size(), temporary, &response); err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	return remoteGiteaAsset(response), nil
}

func (g *giteaForge) DownloadReleaseAsset(ctx context.Context, ref forge.RepositoryRef, asset forge.RemoteReleaseAsset) (io.ReadCloser, error) {
	endpoint, err := url.Parse(asset.URL)
	server, serverErr := url.Parse(g.baseURL)
	expectedPrefix := "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name) + "/releases/download/"
	if err != nil || serverErr != nil || !endpoint.IsAbs() || !forge.SameOrigin(server, endpoint) || !strings.HasPrefix(endpoint.EscapedPath(), expectedPrefix) || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("gitea returned an untrusted release asset download URL")
	}
	return g.requester.DownloadReader(ctx, endpoint.String(), "application/octet-stream")
}

func remoteGiteaRelease(release giteaRelease) forge.RemoteRelease {
	return forge.RemoteRelease{ID: strconv.FormatInt(release.ID, 10), TagName: release.TagName, TargetCommit: release.TargetCommitish, Title: release.Name, Notes: release.Body, URL: release.URL, Draft: release.Draft, Prerelease: release.Prerelease}
}

func remoteGiteaAsset(asset giteaReleaseAsset) forge.RemoteReleaseAsset {
	return forge.RemoteReleaseAsset{ID: strconv.FormatInt(asset.ID, 10), Name: asset.Name, Size: asset.Size, URL: asset.BrowserURL}
}

func giteaRepoAPIPath(ref forge.RepositoryRef) string {
	return "/api/v1/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}

func giteaArchiveAPIPath(ref forge.RepositoryRef, revision string) string {
	return giteaRepoAPIPath(ref) + "/archive/" + url.PathEscape(revision) + ".zip"
}

func parseGiteaRepository(value string) (string, string, error) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("repository must be written as OWNER/REPO")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
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

type giteaForge struct {
	baseURL   string
	requester *httpRequester
}

var (
	_ Forge             = (*giteaForge)(nil)
	_ ForgeSnapshotter  = (*giteaForge)(nil)
	_ ForgeCommitWriter = (*giteaForge)(nil)
)

func newGiteaForge(p profile) (*giteaForge, error) {
	server, err := normalizeServerURL(p.URL)
	if err != nil {
		return nil, err
	}
	p.Provider = ForgeGitea
	if p.AuthKind != AuthToken && p.AuthKind != AuthBearer {
		return nil, fmt.Errorf("gitea does not support authentication kind %q", p.AuthKind)
	}
	auth := func(request *http.Request) {
		prefix := "token "
		if p.AuthKind == AuthBearer {
			prefix = "Bearer "
		}
		request.Header.Set("Authorization", prefix+p.Token)
	}
	return &giteaForge{
		baseURL:   server,
		requester: newHTTPRequester(p, server, auth, make(http.Header)),
	}, nil
}

func (g *giteaForge) Kind() ForgeKind { return ForgeGitea }

func (g *giteaForge) Capabilities() ForgeCapabilities {
	return ForgeCapabilities{BranchCreate: true, Push: true}
}

func (g *giteaForge) Probe(ctx context.Context) error {
	var version struct {
		Version string `json:"version"`
	}
	return g.requester.doJSON(ctx, http.MethodGet, "/api/v1/version", nil, &version)
}

func (g *giteaForge) ResolveRepository(ctx context.Context, value string) (RepositoryRef, RepositoryInfo, error) {
	owner, name, err := parseGiteaRepository(value)
	if err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	ref := RepositoryRef{Forge: ForgeGitea, Server: g.baseURL, Namespace: owner, Name: name, Canonical: owner + "/" + name}
	var response giteaRepository
	if err := g.requester.doJSON(ctx, http.MethodGet, giteaRepoAPIPath(ref), nil, &response); err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	return ref, RepositoryInfo{DefaultBranch: response.DefaultBranch, Empty: response.Empty}, nil
}

func (g *giteaForge) Head(ctx context.Context, ref RepositoryRef, branch string) (string, error) {
	var response giteaBranchResponse
	endpoint := giteaRepoAPIPath(ref) + "/branches/" + url.PathEscape(branch)
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	commit := response.commitSHA()
	if commit == "" {
		return "", fmt.Errorf("gitea returned no commit ID for branch %q", branch)
	}
	return commit, nil
}

func (g *giteaForge) Tree(ctx context.Context, ref RepositoryRef, commit string) (map[string]RemoteFile, error) {
	result := make(map[string]RemoteFile)
	for pageNumber := 1; ; pageNumber++ {
		endpoint := fmt.Sprintf("%s/git/trees/%s?recursive=true&page=%d&per_page=1000", giteaRepoAPIPath(ref), url.PathEscape(commit), pageNumber)
		var response giteaTreeResponse
		if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return nil, err
		}
		for _, entry := range response.Tree {
			if entry.Type != "blob" {
				continue
			}
			cleaned, err := validateRemotePath(entry.Path)
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
			result[cleaned] = RemoteFile{BlobID: entry.SHA, Mode: mode, Size: entry.Size}
		}
		if !response.Truncated || len(response.Tree) == 0 {
			break
		}
	}
	return result, nil
}

func (g *giteaForge) Blob(ctx context.Context, ref RepositoryRef, file RemoteFile) ([]byte, error) {
	var response giteaBlobResponse
	endpoint := giteaRepoAPIPath(ref) + "/git/blobs/" + url.PathEscape(file.BlobID)
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "" && response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported blob encoding %q", response.Encoding)
	}
	content := strings.ReplaceAll(response.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decode blob %s: %w", file.BlobID, err)
	}
	return decoded, nil
}

func (g *giteaForge) Snapshot(ctx context.Context, ref RepositoryRef, revision string) ([]byte, error) {
	return g.requester.download(ctx, giteaArchiveAPIPath(ref, revision))
}

func (g *giteaForge) CommitDetails(ctx context.Context, ref RepositoryRef, commit string) (RemoteCommit, error) {
	var response giteaCommitDetails
	endpoint := giteaRepoAPIPath(ref) + "/git/commits/" + url.PathEscape(commit) + "?stat=false&verification=false&files=true"
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return RemoteCommit{}, err
	}
	result := RemoteCommit{ID: response.SHA, Message: response.Commit.Message}
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

func (g *giteaForge) ApplyCommit(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if !g.Capabilities().Push {
		return ApplyCommitResult{}, ErrUnsupported
	}
	operations := make([]giteaChangeOperation, 0, len(request.Changes))
	for _, change := range request.Changes {
		operation := giteaChangeOperation{Operation: change.Operation, Path: change.Path, SHA: change.BlobID}
		if change.Operation != "delete" {
			operation.Content = base64.StdEncoding.EncodeToString(change.Content)
		}
		operations = append(operations, operation)
	}
	payload := giteaChangeFilesRequest{Branch: request.Branch, NewBranch: request.NewBranch, Message: request.Message, Files: operations}
	var response json.RawMessage
	if err := g.requester.doJSON(ctx, http.MethodPost, giteaRepoAPIPath(request.Repository)+"/contents", payload, &response); err != nil {
		var remoteErr *RemoteError
		if errors.As(err, &remoteErr) && (remoteErr.Status == http.StatusNotFound || remoteErr.Status == http.StatusMethodNotAllowed) {
			return ApplyCommitResult{}, fmt.Errorf("%w; this gitea version may not support atomic multi-file changes", err)
		}
		if remoteErrorHasStatus(err, http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity) {
			err = confirmStaleHead(ctx, g, request.Repository, request.Branch, request.ExpectedHead, err)
		}
		return ApplyCommitResult{}, err
	}
	target := request.Branch
	if request.NewBranch != "" {
		target = request.NewBranch
	}
	commitID, err := g.Head(ctx, request.Repository, target)
	if err != nil {
		return ApplyCommitResult{}, fmt.Errorf("commit may have been submitted, but refreshing branch state failed: %w", err)
	}
	details, err := g.CommitDetails(ctx, request.Repository, commitID)
	if err != nil {
		return ApplyCommitResult{}, fmt.Errorf("commit %s was submitted, but reading its parents failed: %w", commitID, err)
	}
	return ApplyCommitResult{CommitID: commitID, ParentIDs: details.ParentIDs, ConditionalRef: false}, nil
}

func giteaRepoAPIPath(ref RepositoryRef) string {
	return "/api/v1/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}

func giteaArchiveAPIPath(ref RepositoryRef, revision string) string {
	return giteaRepoAPIPath(ref) + "/archive/" + url.PathEscape(revision) + ".zip"
}

func parseGiteaRepository(value string) (string, string, error) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("repository must be written as OWNER/REPO")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

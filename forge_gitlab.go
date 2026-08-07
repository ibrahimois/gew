package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const gitLabPushVerified = false

type gitLabForge struct {
	server    string
	apiBase   string
	requester *httpRequester
}

type gitLabProject struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	EmptyRepo         bool   `json:"empty_repo"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

type gitLabCommit struct {
	ID        string   `json:"id"`
	Message   string   `json:"message"`
	ParentIDs []string `json:"parent_ids"`
}

type gitLabTreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type gitLabBlob struct {
	ID       string `json:"id"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

type gitLabFileMetadata struct {
	BlobID        string `json:"blob_id"`
	CommitID      string `json:"commit_id"`
	LastCommitID  string `json:"last_commit_id"`
	Size          int64  `json:"size"`
	ExecuteMode   bool   `json:"execute_filemode"`
	FilePath      string `json:"file_path"`
	ContentSHA256 string `json:"content_sha256"`
}

type gitLabDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	DeletedFile bool   `json:"deleted_file"`
	RenamedFile bool   `json:"renamed_file"`
}

func newGitLabForge(p profile) (*gitLabForge, error) {
	server, err := normalizeServerURL(p.URL)
	if err != nil {
		return nil, err
	}
	p.Provider = ForgeGitLab
	if p.AuthKind == "" {
		p.AuthKind = AuthBearer
	}
	if p.AuthKind != AuthBearer && p.AuthKind != AuthPrivate {
		return nil, fmt.Errorf("gitlab supports bearer or private-token authentication, got %q", p.AuthKind)
	}
	apiBase := strings.TrimRight(server, "/")
	if !strings.HasSuffix(strings.ToLower(apiBase), "/api/v4") {
		apiBase += "/api/v4"
	}
	auth := func(request *http.Request) {
		if p.AuthKind == AuthPrivate {
			request.Header.Set("Private-Token", p.Token)
		} else {
			request.Header.Set("Authorization", "Bearer "+p.Token)
		}
	}
	return &gitLabForge{server: server, apiBase: apiBase, requester: newHTTPRequester(p, apiBase, auth, make(http.Header))}, nil
}

func (g *gitLabForge) Kind() ForgeKind { return ForgeGitLab }

func (g *gitLabForge) Capabilities() ForgeCapabilities {
	return ForgeCapabilities{ArchiveSnapshot: true, AtomicMultiFile: true, ConditionalRef: false, BranchCreate: true, Push: gitLabPushVerified}
}

func (g *gitLabForge) Probe(ctx context.Context) error {
	var user struct {
		ID int64 `json:"id"`
	}
	return g.requester.doJSON(ctx, http.MethodGet, "/user", nil, &user)
}

func (g *gitLabForge) ResolveRepository(ctx context.Context, value string) (RepositoryRef, RepositoryInfo, error) {
	fullPath, err := parseGitLabRepository(g.server, value)
	if err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	var project gitLabProject
	if err := g.requester.doJSON(ctx, http.MethodGet, "/projects/"+url.PathEscape(fullPath), nil, &project); err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	if project.ID == 0 || project.Path == "" || project.PathWithNamespace == "" {
		return RepositoryRef{}, RepositoryInfo{}, errors.New("gitlab returned incomplete project identity")
	}
	namespace := project.Namespace.FullPath
	if namespace == "" {
		namespace = strings.TrimSuffix(project.PathWithNamespace, "/"+project.Path)
	}
	ref := RepositoryRef{
		Forge: ForgeGitLab, Server: g.server, Namespace: namespace, Name: project.Path,
		RemoteID: strconv.FormatInt(project.ID, 10), Canonical: project.PathWithNamespace,
	}
	return ref, RepositoryInfo{DefaultBranch: project.DefaultBranch, Empty: project.EmptyRepo}, nil
}

func (g *gitLabForge) Head(ctx context.Context, ref RepositoryRef, branch string) (string, error) {
	var response struct {
		Commit gitLabCommit `json:"commit"`
	}
	endpoint := gitLabProjectAPIPath(ref) + "/repository/branches/" + url.PathEscape(strings.TrimPrefix(branch, "refs/heads/"))
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	if response.Commit.ID == "" {
		return "", fmt.Errorf("gitlab returned no commit ID for branch %q", branch)
	}
	return response.Commit.ID, nil
}

func (g *gitLabForge) Tree(ctx context.Context, ref RepositoryRef, commit string) (map[string]RemoteFile, error) {
	result := make(map[string]RemoteFile)
	for pageNumber := 1; ; pageNumber++ {
		query := url.Values{"ref": {commit}, "recursive": {"true"}, "per_page": {"100"}, "page": {strconv.Itoa(pageNumber)}}
		endpoint := gitLabProjectAPIPath(ref) + "/repository/tree?" + query.Encode()
		var entries []gitLabTreeEntry
		if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type != "blob" {
				continue
			}
			cleaned, err := validateRemotePath(entry.Path)
			if err != nil {
				return nil, err
			}
			mode, _ := strconv.ParseUint(entry.Mode, 8, 32)
			result[cleaned] = RemoteFile{BlobID: entry.ID, Mode: uint32(mode)}
		}
		if len(entries) < 100 {
			break
		}
	}
	return result, nil
}

func (g *gitLabForge) Blob(ctx context.Context, ref RepositoryRef, file RemoteFile) ([]byte, error) {
	var response gitLabBlob
	endpoint := gitLabProjectAPIPath(ref) + "/repository/blobs/" + url.PathEscape(file.BlobID)
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported gitlab blob encoding %q", response.Encoding)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode gitlab blob %s: %w", file.BlobID, err)
	}
	return content, nil
}

func (g *gitLabForge) Snapshot(ctx context.Context, ref RepositoryRef, revision string) ([]byte, error) {
	endpoint := gitLabProjectAPIPath(ref) + "/repository/archive.zip?" + url.Values{"sha": {revision}}.Encode()
	return g.requester.download(ctx, endpoint)
}

func (g *gitLabForge) CommitDetails(ctx context.Context, ref RepositoryRef, commit string) (RemoteCommit, error) {
	var response gitLabCommit
	endpoint := gitLabProjectAPIPath(ref) + "/repository/commits/" + url.PathEscape(commit) + "?stats=false"
	if err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return RemoteCommit{}, err
	}
	result := RemoteCommit{ID: response.ID, Message: response.Message, ParentIDs: append([]string(nil), response.ParentIDs...)}
	for pageNumber := 1; ; pageNumber++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(pageNumber)}}
		var diffs []gitLabDiff
		diffEndpoint := gitLabProjectAPIPath(ref) + "/repository/commits/" + url.PathEscape(commit) + "/diff?" + query.Encode()
		if err := g.requester.doJSON(ctx, http.MethodGet, diffEndpoint, nil, &diffs); err != nil {
			return RemoteCommit{}, err
		}
		for _, diff := range diffs {
			filePath := diff.NewPath
			if diff.DeletedFile {
				filePath = diff.OldPath
			}
			cleaned, err := validateRemotePath(filePath)
			if err != nil {
				return RemoteCommit{}, err
			}
			result.Paths = append(result.Paths, cleaned)
		}
		if len(diffs) < 100 {
			break
		}
	}
	return result, nil
}

func (g *gitLabForge) ApplyCommit(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if !gitLabPushVerified {
		return ApplyCommitResult{}, fmt.Errorf("gitlab push is disabled until live same-file concurrency tests pass: %w", ErrUnsupported)
	}
	return g.applyCommitUnchecked(ctx, request)
}

func (g *gitLabForge) applyCommitUnchecked(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if request.ExpectedHead == "" {
		return ApplyCommitResult{}, fmt.Errorf("gitlab empty-repository push is not verified: %w", ErrUnsupported)
	}
	current, err := g.Head(ctx, request.Repository, request.Branch)
	if err != nil {
		return ApplyCommitResult{}, err
	}
	if current != request.ExpectedHead {
		return ApplyCommitResult{}, ErrStaleHead
	}
	type action struct {
		Action       string `json:"action"`
		FilePath     string `json:"file_path"`
		Content      string `json:"content,omitempty"`
		Encoding     string `json:"encoding,omitempty"`
		LastCommitID string `json:"last_commit_id,omitempty"`
	}
	actions := make([]action, 0, len(request.Changes))
	seen := make(map[string]bool)
	for _, change := range request.Changes {
		cleaned, err := validateRemotePath(change.Path)
		if err != nil {
			return ApplyCommitResult{}, err
		}
		if seen[cleaned] {
			return ApplyCommitResult{}, fmt.Errorf("duplicate repository path %q", cleaned)
		}
		seen[cleaned] = true
		item := action{Action: change.Operation, FilePath: cleaned}
		switch change.Operation {
		case "create":
			item.Content = base64.StdEncoding.EncodeToString(change.Content)
			item.Encoding = "base64"
		case "update", "delete":
			metadata, err := g.fileMetadata(ctx, request.Repository, cleaned, request.ExpectedHead)
			if err != nil {
				return ApplyCommitResult{}, err
			}
			if metadata.LastCommitID == "" {
				return ApplyCommitResult{}, fmt.Errorf("gitlab returned no last_commit_id for %s", cleaned)
			}
			item.LastCommitID = metadata.LastCommitID
			if change.Operation == "update" {
				item.Content = base64.StdEncoding.EncodeToString(change.Content)
				item.Encoding = "base64"
			}
		default:
			return ApplyCommitResult{}, fmt.Errorf("unsupported remote change operation %q", change.Operation)
		}
		actions = append(actions, item)
	}
	payload := struct {
		Branch        string   `json:"branch"`
		CommitMessage string   `json:"commit_message"`
		StartSHA      string   `json:"start_sha,omitempty"`
		Actions       []action `json:"actions"`
	}{Branch: request.Branch, CommitMessage: request.Message, Actions: actions}
	if request.NewBranch != "" {
		payload.Branch = request.NewBranch
		payload.StartSHA = request.ExpectedHead
	}
	var response gitLabCommit
	endpoint := gitLabProjectAPIPath(request.Repository) + "/repository/commits"
	if err := g.requester.doJSON(ctx, http.MethodPost, endpoint, payload, &response); err != nil {
		return ApplyCommitResult{}, err
	}
	if response.ID == "" {
		return ApplyCommitResult{}, errors.New("gitlab returned an empty commit ID")
	}
	return ApplyCommitResult{CommitID: response.ID, ParentIDs: response.ParentIDs, ConditionalRef: false}, nil
}

func (g *gitLabForge) fileMetadata(ctx context.Context, ref RepositoryRef, filePath, commit string) (gitLabFileMetadata, error) {
	var response gitLabFileMetadata
	endpoint := gitLabProjectAPIPath(ref) + "/repository/files/" + url.PathEscape(filePath) + "?" + url.Values{"ref": {commit}}.Encode()
	err := g.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response)
	return response, err
}

func gitLabProjectAPIPath(ref RepositoryRef) string {
	identifier := ref.RemoteID
	if identifier == "" {
		identifier = ref.Canonical
	}
	return "/projects/" + url.PathEscape(identifier)
}

func parseGitLabRepository(server, value string) (string, error) {
	value = strings.TrimSpace(value)
	serverURL, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	projectPath := value
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.IsAbs() {
		if !strings.EqualFold(parsed.Hostname(), serverURL.Hostname()) {
			return "", fmt.Errorf("project host %s does not match profile host %s", parsed.Hostname(), serverURL.Hostname())
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", errors.New("gitlab project URL must not contain a query or fragment")
		}
		projectPath = strings.Trim(parsed.Path, "/")
	}
	projectPath = strings.TrimSuffix(strings.Trim(projectPath, "/"), ".git")
	if _, numericErr := strconv.ParseInt(projectPath, 10, 64); numericErr == nil {
		return projectPath, nil
	}
	parts := strings.Split(projectPath, "/")
	if len(parts) < 2 {
		return "", errors.New("GitLab project must include a namespace and repository")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("GitLab project path contains an invalid segment")
		}
	}
	return strings.Join(parts, "/"), nil
}

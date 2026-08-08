package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"gew/internal/forge"
)

const githubAPIVersion = "2026-03-10"

type githubForge struct {
	server    string
	apiBase   string
	requester *forge.HTTPRequester
}

var (
	_ forge.Forge                 = (*githubForge)(nil)
	_ forge.ForgeSnapshotter      = (*githubForge)(nil)
	_ forge.ForgeCommitWriter     = (*githubForge)(nil)
	_ forge.ForgeReleasePublisher = (*githubForge)(nil)
)

type githubRelease struct {
	ID              int64  `json:"id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	HTMLURL         string `json:"html_url"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

type githubReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRepository struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	DefaultBranch string  `json:"default_branch"`
	PushedAt      *string `json:"pushed_at"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type githubRefResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type githubTreeResponse struct {
	SHA       string            `json:"sha"`
	Tree      []githubTreeEntry `json:"tree"`
	Truncated bool              `json:"truncated"`
}

type githubBlobResponse struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type githubGitCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Tree    struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

type githubCommitDetails struct {
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

func New(p forge.Config) (*githubForge, error) {
	server, err := forge.NormalizeServerURL(p.URL)
	if err != nil {
		return nil, err
	}
	p.Provider = forge.ForgeGitHub
	if p.AuthKind != forge.AuthBearer {
		return nil, fmt.Errorf("github requires bearer authentication, got %q", p.AuthKind)
	}
	apiBase, err := githubAPIBase(server)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("X-GitHub-Api-Version", githubAPIVersion)
	auth := func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+p.Token) }
	requester, err := forge.NewHTTPRequester(p, apiBase, auth, headers)
	if err != nil {
		return nil, err
	}
	return &githubForge{server: server, apiBase: apiBase, requester: requester}, nil
}

func githubAPIBase(server string) (string, error) {
	parsed, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(parsed.Hostname(), "github.com") {
		return "https://api.github.com", nil
	}
	base := strings.TrimRight(server, "/")
	if strings.HasSuffix(strings.ToLower(base), "/api/v3") {
		return base, nil
	}
	return base + "/api/v3", nil
}

func (g *githubForge) Kind() forge.ForgeKind { return forge.ForgeGitHub }

func (g *githubForge) Capabilities() forge.ForgeCapabilities {
	return forge.ForgeCapabilities{BranchCreate: true, Push: true}
}

func (g *githubForge) Probe(ctx context.Context) error {
	var user struct {
		Login string `json:"login"`
	}
	return g.requester.DoJSON(ctx, http.MethodGet, "/user", nil, &user)
}

func (g *githubForge) ResolveRepository(ctx context.Context, value string) (forge.RepositoryRef, forge.RepositoryInfo, error) {
	owner, name, err := parseGitHubRepository(g.server, value)
	if err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	initial := forge.RepositoryRef{Forge: forge.ForgeGitHub, Server: g.server, Namespace: owner, Name: name}
	var response githubRepository
	if err := g.requester.DoJSON(ctx, http.MethodGet, githubRepoAPIPath(initial), nil, &response); err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	if response.Owner.Login == "" || response.Name == "" {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, errors.New("github returned incomplete repository identity")
	}
	canonical := response.FullName
	if canonical == "" {
		canonical = response.Owner.Login + "/" + response.Name
	}
	ref := forge.RepositoryRef{
		Forge: forge.ForgeGitHub, Server: g.server, Namespace: response.Owner.Login,
		Name: response.Name, RemoteID: strconv.FormatInt(response.ID, 10), Canonical: canonical,
	}
	return ref, forge.RepositoryInfo{DefaultBranch: response.DefaultBranch, Empty: response.PushedAt == nil}, nil
}

func (g *githubForge) Head(ctx context.Context, ref forge.RepositoryRef, branch string) (string, error) {
	var response githubRefResponse
	endpoint := githubRepoAPIPath(ref) + "/git/ref/" + url.PathEscape("heads/"+strings.TrimPrefix(branch, "refs/heads/"))
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		var remoteErr *forge.RemoteError
		if errors.As(err, &remoteErr) && remoteErr.Status == http.StatusConflict {
			return "", forge.ErrNotFound
		}
		return "", err
	}
	if response.Object.SHA == "" {
		return "", fmt.Errorf("github returned no commit ID for branch %q", branch)
	}
	return response.Object.SHA, nil
}

func (g *githubForge) Tree(ctx context.Context, ref forge.RepositoryRef, commit string) (map[string]forge.RemoteFile, error) {
	gitCommit, err := g.gitCommit(ctx, ref, commit)
	if err != nil {
		return nil, err
	}
	if gitCommit.Tree.SHA == "" {
		return nil, fmt.Errorf("github commit %s has no tree", commit)
	}
	var response githubTreeResponse
	endpoint := githubRepoAPIPath(ref) + "/git/trees/" + url.PathEscape(gitCommit.Tree.SHA) + "?recursive=1"
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Truncated {
		return g.walkTree(ctx, ref, gitCommit.Tree.SHA)
	}
	return githubRemoteFiles(response.Tree)
}

func githubRemoteFiles(entries []githubTreeEntry) (map[string]forge.RemoteFile, error) {
	result := make(map[string]forge.RemoteFile)
	for _, entry := range entries {
		if entry.Type != "blob" {
			continue
		}
		cleaned, err := forge.ValidateRemotePath(entry.Path)
		if err != nil {
			return nil, err
		}
		mode, _ := strconv.ParseUint(entry.Mode, 8, 32)
		result[cleaned] = forge.RemoteFile{BlobID: entry.SHA, Mode: uint32(mode), Size: entry.Size}
	}
	return result, nil
}

func (g *githubForge) walkTree(ctx context.Context, ref forge.RepositoryRef, rootTree string) (map[string]forge.RemoteFile, error) {
	type pendingTree struct{ sha, prefix string }
	pending := []pendingTree{{sha: rootTree}}
	result := make(map[string]forge.RemoteFile)
	seen := make(map[string]bool)
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		key := current.sha + "\x00" + current.prefix
		if seen[key] {
			continue
		}
		seen[key] = true
		var response githubTreeResponse
		endpoint := githubRepoAPIPath(ref) + "/git/trees/" + url.PathEscape(current.sha)
		if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return nil, err
		}
		if response.Truncated {
			return nil, errors.New("github returned a truncated non-recursive tree")
		}
		for _, entry := range response.Tree {
			combined := path.Join(current.prefix, entry.Path)
			cleaned, err := forge.ValidateRemotePath(combined)
			if err != nil {
				return nil, err
			}
			switch entry.Type {
			case "tree":
				pending = append(pending, pendingTree{sha: entry.SHA, prefix: cleaned})
			case "blob":
				mode, _ := strconv.ParseUint(entry.Mode, 8, 32)
				result[cleaned] = forge.RemoteFile{BlobID: entry.SHA, Mode: uint32(mode), Size: entry.Size}
			}
			if len(result)+len(pending) > 1_000_000 {
				return nil, errors.New("github repository tree exceeds one million entries")
			}
		}
	}
	return result, nil
}

func (g *githubForge) Blob(ctx context.Context, ref forge.RepositoryRef, file forge.RemoteFile) ([]byte, error) {
	var response githubBlobResponse
	endpoint := githubRepoAPIPath(ref) + "/git/blobs/" + url.PathEscape(file.BlobID)
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported github blob encoding %q", response.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode github blob %s: %w", file.BlobID, err)
	}
	return decoded, nil
}

func (g *githubForge) Snapshot(ctx context.Context, ref forge.RepositoryRef, revision string) ([]byte, error) {
	endpoint := githubRepoAPIPath(ref) + "/zipball/" + url.PathEscape(revision)
	return g.requester.Download(ctx, endpoint)
}

func (g *githubForge) CommitDetails(ctx context.Context, ref forge.RepositoryRef, commit string) (forge.RemoteCommit, error) {
	result := forge.RemoteCommit{}
	for pageNumber := 1; ; pageNumber++ {
		var response githubCommitDetails
		endpoint := fmt.Sprintf("%s/commits/%s?per_page=100&page=%d", githubRepoAPIPath(ref), url.PathEscape(commit), pageNumber)
		if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return forge.RemoteCommit{}, err
		}
		if pageNumber == 1 {
			result.ID = response.SHA
			result.Message = response.Commit.Message
			for _, parent := range response.Parents {
				result.ParentIDs = append(result.ParentIDs, parent.SHA)
			}
		}
		for _, file := range response.Files {
			cleaned, err := forge.ValidateRemotePath(file.Filename)
			if err != nil {
				return forge.RemoteCommit{}, err
			}
			result.Paths = append(result.Paths, cleaned)
		}
		if len(response.Files) < 100 {
			break
		}
	}
	if result.ID == "" {
		result.ID = commit
	}
	return result, nil
}

func (g *githubForge) ApplyCommit(ctx context.Context, request forge.ApplyCommitRequest) (forge.ApplyCommitResult, error) {
	if request.ExpectedHead == "" {
		return forge.ApplyCommitResult{}, fmt.Errorf("github cannot create the first branch in an empty repository through the REST Git database API: %w", forge.ErrUnsupported)
	}
	currentHead, err := g.Head(ctx, request.Repository, request.Branch)
	if err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if currentHead != request.ExpectedHead {
		return forge.ApplyCommitResult{}, forge.ErrStaleHead
	}
	base, err := g.gitCommit(ctx, request.Repository, request.ExpectedHead)
	if err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if base.Tree.SHA == "" {
		return forge.ApplyCommitResult{}, errors.New("github base commit has no tree")
	}

	type treeItem struct {
		Path string  `json:"path"`
		Mode string  `json:"mode"`
		Type string  `json:"type"`
		SHA  *string `json:"sha"`
	}
	items := make([]treeItem, 0, len(request.Changes))
	for _, change := range request.Changes {
		mode := "100644"
		if change.Mode&0o111 != 0 {
			mode = "100755"
		}
		item := treeItem{Path: change.Path, Mode: mode, Type: "blob"}
		switch change.Operation {
		case "delete":
			item.SHA = nil
		case "create", "update":
			var blob githubBlobResponse
			payload := map[string]string{"content": base64.StdEncoding.EncodeToString(change.Content), "encoding": "base64"}
			if err := g.requester.DoJSON(ctx, http.MethodPost, githubRepoAPIPath(request.Repository)+"/git/blobs", payload, &blob); err != nil {
				return forge.ApplyCommitResult{}, err
			}
			if blob.SHA == "" {
				return forge.ApplyCommitResult{}, errors.New("github returned an empty blob ID")
			}
			item.SHA = &blob.SHA
		default:
			return forge.ApplyCommitResult{}, fmt.Errorf("unsupported remote change operation %q", change.Operation)
		}
		items = append(items, item)
	}
	var tree githubTreeResponse
	treePayload := struct {
		BaseTree string     `json:"base_tree"`
		Tree     []treeItem `json:"tree"`
	}{BaseTree: base.Tree.SHA, Tree: items}
	if err := g.requester.DoJSON(ctx, http.MethodPost, githubRepoAPIPath(request.Repository)+"/git/trees", treePayload, &tree); err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if tree.SHA == "" {
		return forge.ApplyCommitResult{}, errors.New("github returned an empty tree ID")
	}
	var created githubGitCommit
	commitPayload := struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{Message: request.Message, Tree: tree.SHA, Parents: []string{request.ExpectedHead}}
	if err := g.requester.DoJSON(ctx, http.MethodPost, githubRepoAPIPath(request.Repository)+"/git/commits", commitPayload, &created); err != nil {
		return forge.ApplyCommitResult{}, err
	}
	if created.SHA == "" {
		return forge.ApplyCommitResult{}, errors.New("github returned an empty commit ID")
	}

	if request.NewBranch != "" {
		payload := map[string]string{"ref": "refs/heads/" + strings.TrimPrefix(request.NewBranch, "refs/heads/"), "sha": created.SHA}
		var response githubRefResponse
		if err := g.requester.DoJSON(ctx, http.MethodPost, githubRepoAPIPath(request.Repository)+"/git/refs", payload, &response); err != nil {
			return forge.ApplyCommitResult{}, err
		}
		if response.Object.SHA != created.SHA {
			return forge.ApplyCommitResult{}, errors.New("github created branch points to an unexpected commit")
		}
	} else {
		branch := strings.TrimPrefix(request.Branch, "refs/heads/")
		payload := struct {
			SHA   string `json:"sha"`
			Force bool   `json:"force"`
		}{SHA: created.SHA, Force: false}
		var response githubRefResponse
		endpoint := githubRepoAPIPath(request.Repository) + "/git/refs/" + url.PathEscape("heads/"+branch)
		if err := g.requester.DoJSON(ctx, http.MethodPatch, endpoint, payload, &response); err != nil {
			if forge.RemoteErrorHasStatus(err, http.StatusConflict, http.StatusUnprocessableEntity) {
				err = forge.ConfirmStaleHead(ctx, g, request.Repository, request.Branch, request.ExpectedHead, err)
			}
			return forge.ApplyCommitResult{}, err
		}
		if response.Object.SHA != created.SHA {
			return forge.ApplyCommitResult{}, errors.New("github updated branch points to an unexpected commit")
		}
	}
	return forge.ApplyCommitResult{CommitID: created.SHA, ParentIDs: []string{request.ExpectedHead}, ConditionalRef: true}, nil
}

func (g *githubForge) FindReleaseByTag(ctx context.Context, ref forge.RepositoryRef, tag string) (forge.RemoteRelease, error) {
	var response githubRelease
	endpoint := githubRepoAPIPath(ref) + "/releases/tags/" + url.PathEscape(tag)
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return forge.RemoteRelease{}, err
	}
	return remoteGitHubRelease(response), nil
}

func (g *githubForge) CreateRelease(ctx context.Context, request forge.CreateReleaseRequest) (forge.RemoteRelease, error) {
	makeLatest := "false"
	if request.Latest {
		makeLatest = "true"
	}
	payload := struct {
		TagName         string `json:"tag_name"`
		TargetCommitish string `json:"target_commitish"`
		Name            string `json:"name"`
		Body            string `json:"body"`
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
		MakeLatest      string `json:"make_latest"`
	}{request.TagName, request.TargetCommit, request.Title, request.Notes, request.Draft, request.Prerelease, makeLatest}
	var response githubRelease
	if err := g.requester.DoJSON(ctx, http.MethodPost, githubRepoAPIPath(request.Repository)+"/releases", payload, &response); err != nil {
		return forge.RemoteRelease{}, err
	}
	return remoteGitHubRelease(response), nil
}

func (g *githubForge) ListReleaseAssets(ctx context.Context, ref forge.RepositoryRef, releaseID string) ([]forge.RemoteReleaseAsset, error) {
	var response []githubReleaseAsset
	endpoint := githubRepoAPIPath(ref) + "/releases/" + url.PathEscape(releaseID) + "/assets?per_page=100"
	if err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	assets := make([]forge.RemoteReleaseAsset, 0, len(response))
	for _, asset := range response {
		assets = append(assets, remoteGitHubAsset(asset))
	}
	return assets, nil
}

func (g *githubForge) UploadReleaseAsset(ctx context.Context, ref forge.RepositoryRef, releaseID, name string, size int64, content io.Reader) (forge.RemoteReleaseAsset, error) {
	endpoint := g.uploadBase() + githubRepoAPIPath(ref) + "/releases/" + url.PathEscape(releaseID) + "/assets?name=" + url.QueryEscape(name)
	var response githubReleaseAsset
	if err := g.requester.DoBody(ctx, http.MethodPost, endpoint, "application/octet-stream", size, content, &response); err != nil {
		return forge.RemoteReleaseAsset{}, err
	}
	return remoteGitHubAsset(response), nil
}

func (g *githubForge) DownloadReleaseAsset(ctx context.Context, ref forge.RepositoryRef, asset forge.RemoteReleaseAsset) (io.ReadCloser, error) {
	endpoint := githubRepoAPIPath(ref) + "/releases/assets/" + url.PathEscape(asset.ID)
	return g.requester.DownloadReader(ctx, endpoint, "application/octet-stream")
}

func (g *githubForge) uploadBase() string {
	parsed, _ := url.Parse(g.server)
	if strings.EqualFold(parsed.Hostname(), "github.com") {
		return "https://uploads.github.com"
	}
	return g.apiBase
}

func remoteGitHubRelease(release githubRelease) forge.RemoteRelease {
	return forge.RemoteRelease{ID: strconv.FormatInt(release.ID, 10), TagName: release.TagName, TargetCommit: release.TargetCommitish, Title: release.Name, Notes: release.Body, URL: release.HTMLURL, Draft: release.Draft, Prerelease: release.Prerelease}
}

func remoteGitHubAsset(asset githubReleaseAsset) forge.RemoteReleaseAsset {
	return forge.RemoteReleaseAsset{ID: strconv.FormatInt(asset.ID, 10), Name: asset.Name, Size: asset.Size, URL: asset.BrowserDownloadURL}
}

func (g *githubForge) gitCommit(ctx context.Context, ref forge.RepositoryRef, commit string) (githubGitCommit, error) {
	var response githubGitCommit
	endpoint := githubRepoAPIPath(ref) + "/git/commits/" + url.PathEscape(commit)
	err := g.requester.DoJSON(ctx, http.MethodGet, endpoint, nil, &response)
	return response, err
}

func githubRepoAPIPath(ref forge.RepositoryRef) string {
	return "/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}

func parseGitHubRepository(server, value string) (string, string, error) {
	value = strings.TrimSpace(value)
	serverURL, err := url.Parse(server)
	if err != nil {
		return "", "", err
	}
	repositoryPath := value
	if strings.EqualFold(serverURL.Hostname(), "github.com") && strings.HasPrefix(strings.ToLower(repositoryPath), "github.com/") {
		repositoryPath = repositoryPath[len("github.com/"):]
	}
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.IsAbs() {
		if !strings.EqualFold(parsed.Hostname(), serverURL.Hostname()) {
			return "", "", fmt.Errorf("repository host %s does not match profile host %s", parsed.Hostname(), serverURL.Hostname())
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", errors.New("github repository URL must not contain a query or fragment")
		}
		repositoryPath = strings.Trim(parsed.EscapedPath(), "/")
		decoded, decodeErr := url.PathUnescape(repositoryPath)
		if decodeErr != nil {
			return "", "", decodeErr
		}
		repositoryPath = decoded
	}
	parts := strings.Split(strings.Trim(repositoryPath, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("GitHub repository must be OWNER/REPO or a canonical repository URL")
	}
	name := strings.TrimSuffix(parts[1], ".git")
	if name == "" {
		return "", "", errors.New("GitHub repository name is empty")
	}
	return parts[0], name, nil
}

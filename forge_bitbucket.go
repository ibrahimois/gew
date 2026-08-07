package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const bitbucketPushVerified = false

type bitbucketForge struct {
	server    string
	apiBase   string
	requester *httpRequester
}

var (
	_ Forge             = (*bitbucketForge)(nil)
	_ ForgeCommitWriter = (*bitbucketForge)(nil)
)

type bitbucketRepository struct {
	UUID      string `json:"uuid"`
	FullName  string `json:"full_name"`
	Slug      string `json:"slug"`
	Workspace struct {
		Slug string `json:"slug"`
	} `json:"workspace"`
	MainBranch *struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
}

type bitbucketCommit struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	Parents []struct {
		Hash string `json:"hash"`
	} `json:"parents"`
}

type bitbucketTreeEntry struct {
	Path       string   `json:"path"`
	Type       string   `json:"type"`
	Size       int64    `json:"size"`
	Attributes []string `json:"attributes"`
	Commit     struct {
		Hash string `json:"hash"`
	} `json:"commit"`
}

type bitbucketTreePage struct {
	Values []bitbucketTreeEntry `json:"values"`
	Next   string               `json:"next"`
}

type bitbucketDiffstat struct {
	Status string `json:"status"`
	Old    *struct {
		Path string `json:"path"`
	} `json:"old"`
	New *struct {
		Path string `json:"path"`
	} `json:"new"`
}

type bitbucketDiffstatPage struct {
	Values []bitbucketDiffstat `json:"values"`
	Next   string              `json:"next"`
}

func newBitbucketForge(p profile) (*bitbucketForge, error) {
	server, err := normalizeServerURL(p.URL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(parsed.Hostname(), "bitbucket.org") {
		return nil, errors.New("bitbucket adapter supports Bitbucket Cloud at bitbucket.org only")
	}
	return newBitbucketForgeWithAPI(p, "https://api.bitbucket.org/2.0")
}

func newBitbucketForgeWithAPI(p profile, apiBase string) (*bitbucketForge, error) {
	p.Provider = ForgeBitbucket
	if p.AuthKind != AuthBearer && p.AuthKind != AuthBasic {
		return nil, fmt.Errorf("bitbucket cloud supports bearer or basic authentication, got %q", p.AuthKind)
	}
	if p.AuthKind == AuthBasic && strings.TrimSpace(p.Username) == "" {
		return nil, errors.New("bitbucket basic authentication requires an Atlassian account email username")
	}
	auth := func(request *http.Request) {
		if p.AuthKind == AuthBasic {
			request.SetBasicAuth(p.Username, p.Token)
		} else {
			request.Header.Set("Authorization", "Bearer "+p.Token)
		}
	}
	server := p.URL
	if server == "" || strings.Contains(server, "127.0.0.1") {
		server = "https://bitbucket.org"
	}
	return &bitbucketForge{server: strings.TrimRight(server, "/"), apiBase: strings.TrimRight(apiBase, "/"), requester: newHTTPRequester(p, apiBase, auth, make(http.Header))}, nil
}

func (b *bitbucketForge) Kind() ForgeKind { return ForgeBitbucket }

func (b *bitbucketForge) Capabilities() ForgeCapabilities {
	return ForgeCapabilities{BranchCreate: true, Push: bitbucketPushVerified}
}

func (b *bitbucketForge) Probe(ctx context.Context) error {
	var user struct {
		UUID string `json:"uuid"`
	}
	return b.requester.doJSON(ctx, http.MethodGet, "/user", nil, &user)
}

func (b *bitbucketForge) ResolveRepository(ctx context.Context, value string) (RepositoryRef, RepositoryInfo, error) {
	workspace, repository, err := parseBitbucketRepository(value)
	if err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	initial := RepositoryRef{Forge: ForgeBitbucket, Server: b.server, Namespace: workspace, Name: repository}
	var response bitbucketRepository
	if err := b.requester.doJSON(ctx, http.MethodGet, bitbucketRepoAPIPath(initial), nil, &response); err != nil {
		return RepositoryRef{}, RepositoryInfo{}, err
	}
	if response.Workspace.Slug == "" || response.Slug == "" || response.UUID == "" {
		return RepositoryRef{}, RepositoryInfo{}, errors.New("bitbucket returned incomplete repository identity")
	}
	canonical := response.FullName
	if canonical == "" {
		canonical = response.Workspace.Slug + "/" + response.Slug
	}
	ref := RepositoryRef{
		Forge: ForgeBitbucket, Server: b.server, Namespace: response.Workspace.Slug,
		Name: response.Slug, RemoteID: response.UUID, Canonical: canonical,
	}
	info := RepositoryInfo{Empty: response.MainBranch == nil}
	if response.MainBranch != nil {
		info.DefaultBranch = response.MainBranch.Name
	}
	return ref, info, nil
}

func (b *bitbucketForge) Head(ctx context.Context, ref RepositoryRef, branch string) (string, error) {
	var response struct {
		Target bitbucketCommit `json:"target"`
	}
	endpoint := bitbucketRepoAPIPath(ref) + "/refs/branches/" + url.PathEscape(strings.TrimPrefix(branch, "refs/heads/"))
	if err := b.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	if response.Target.Hash == "" {
		return "", fmt.Errorf("bitbucket returned no commit hash for branch %q", branch)
	}
	return response.Target.Hash, nil
}

func (b *bitbucketForge) Tree(ctx context.Context, ref RepositoryRef, commit string) (map[string]RemoteFile, error) {
	result := make(map[string]RemoteFile)
	endpoint := bitbucketRepoAPIPath(ref) + "/src/" + url.PathEscape(commit) + "/"
	for endpoint != "" {
		var page bitbucketTreePage
		if err := b.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &page); err != nil {
			return nil, err
		}
		for _, entry := range page.Values {
			cleaned, err := validateRemotePath(entry.Path)
			if err != nil {
				return nil, err
			}
			if entry.Commit.Hash != "" && entry.Commit.Hash != commit {
				return nil, fmt.Errorf("bitbucket tree entry %s resolved to inconsistent commit %s", cleaned, entry.Commit.Hash)
			}
			switch entry.Type {
			case "commit_directory":
				// The root listing with max_depth omitted is paginated but not recursive;
				// queue the exact-commit directory URL after the current page chain.
				result["\x00dir:"+cleaned] = RemoteFile{BlobID: cleaned}
			case "commit_file":
				mode := uint32(0o100644)
				for _, attribute := range entry.Attributes {
					if attribute == "link" || attribute == "subrepository" {
						return nil, fmt.Errorf("bitbucket repository contains unsupported %s at %s", attribute, cleaned)
					}
					if attribute == "executable" {
						mode = 0o100755
					}
				}
				result[cleaned] = RemoteFile{BlobID: bitbucketBlobID(commit, cleaned), Mode: mode, Size: entry.Size}
			}
		}
		if page.Next != "" {
			if err := b.validateNext(page.Next); err != nil {
				return nil, err
			}
		}
		endpoint = page.Next
	}
	directories := make([]string, 0)
	for key := range result {
		if strings.HasPrefix(key, "\x00dir:") {
			directories = append(directories, strings.TrimPrefix(key, "\x00dir:"))
			delete(result, key)
		}
	}
	for len(directories) > 0 {
		directory := directories[0]
		directories = directories[1:]
		endpoint = bitbucketRepoAPIPath(ref) + "/src/" + url.PathEscape(commit) + "/" + escapeRemotePath(directory) + "/"
		for endpoint != "" {
			var page bitbucketTreePage
			if err := b.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &page); err != nil {
				return nil, err
			}
			for _, entry := range page.Values {
				cleaned, err := validateRemotePath(entry.Path)
				if err != nil {
					return nil, err
				}
				if entry.Commit.Hash != "" && entry.Commit.Hash != commit {
					return nil, fmt.Errorf("bitbucket tree entry %s resolved to inconsistent commit %s", cleaned, entry.Commit.Hash)
				}
				if entry.Type == "commit_directory" {
					directories = append(directories, cleaned)
					continue
				}
				if entry.Type != "commit_file" {
					continue
				}
				mode := uint32(0o100644)
				for _, attribute := range entry.Attributes {
					if attribute == "link" || attribute == "subrepository" {
						return nil, fmt.Errorf("bitbucket repository contains unsupported %s at %s", attribute, cleaned)
					}
					if attribute == "executable" {
						mode = 0o100755
					}
				}
				result[cleaned] = RemoteFile{BlobID: bitbucketBlobID(commit, cleaned), Mode: mode, Size: entry.Size}
			}
			if page.Next != "" {
				if err := b.validateNext(page.Next); err != nil {
					return nil, err
				}
			}
			endpoint = page.Next
		}
		if len(result)+len(directories) > 1_000_000 {
			return nil, errors.New("bitbucket repository tree exceeds one million entries")
		}
	}
	return result, nil
}

func (b *bitbucketForge) Blob(ctx context.Context, ref RepositoryRef, file RemoteFile) ([]byte, error) {
	commit, filePath, err := parseBitbucketBlobID(file.BlobID)
	if err != nil {
		return nil, err
	}
	endpoint := bitbucketRepoAPIPath(ref) + "/src/" + url.PathEscape(commit) + "/" + escapeRemotePath(filePath)
	return b.requester.download(ctx, endpoint)
}

func (b *bitbucketForge) CommitDetails(ctx context.Context, ref RepositoryRef, commit string) (RemoteCommit, error) {
	var response bitbucketCommit
	endpoint := bitbucketRepoAPIPath(ref) + "/commit/" + url.PathEscape(commit)
	if err := b.requester.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return RemoteCommit{}, err
	}
	result := RemoteCommit{ID: response.Hash, Message: response.Message}
	for _, parent := range response.Parents {
		result.ParentIDs = append(result.ParentIDs, parent.Hash)
	}
	next := bitbucketRepoAPIPath(ref) + "/diffstat/" + url.PathEscape(commit) + "?pagelen=100"
	for next != "" {
		var page bitbucketDiffstatPage
		if err := b.requester.doJSON(ctx, http.MethodGet, next, nil, &page); err != nil {
			return RemoteCommit{}, err
		}
		for _, item := range page.Values {
			filePath := ""
			if item.New != nil {
				filePath = item.New.Path
			} else if item.Old != nil {
				filePath = item.Old.Path
			}
			cleaned, err := validateRemotePath(filePath)
			if err != nil {
				return RemoteCommit{}, err
			}
			result.Paths = append(result.Paths, cleaned)
		}
		if page.Next != "" {
			if err := b.validateNext(page.Next); err != nil {
				return RemoteCommit{}, err
			}
		}
		next = page.Next
	}
	return result, nil
}

func (b *bitbucketForge) ApplyCommit(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if !bitbucketPushVerified {
		return ApplyCommitResult{}, fmt.Errorf("bitbucket cloud push is disabled until live stale-parent tests pass: %w", ErrUnsupported)
	}
	return b.applyCommitUnchecked(ctx, request)
}

func (b *bitbucketForge) applyCommitUnchecked(ctx context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	if request.ExpectedHead == "" {
		return ApplyCommitResult{}, fmt.Errorf("bitbucket empty-repository push is not verified: %w", ErrUnsupported)
	}
	current, err := b.Head(ctx, request.Repository, request.Branch)
	if err != nil {
		return ApplyCommitResult{}, err
	}
	if current != request.ExpectedHead {
		return ApplyCommitResult{}, ErrStaleHead
	}
	pipeReader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	writeErrors := make(chan error, 1)
	go func() {
		var writeErr error
		defer func() {
			if closeErr := multipartWriter.Close(); writeErr == nil {
				writeErr = closeErr
			}
			_ = pipeWriter.CloseWithError(writeErr)
			writeErrors <- writeErr
		}()
		target := request.Branch
		if request.NewBranch != "" {
			target = request.NewBranch
		}
		for name, value := range map[string]string{"message": request.Message, "branch": target, "parents": request.ExpectedHead} {
			if err := multipartWriter.WriteField(name, value); err != nil {
				writeErr = err
				return
			}
		}
		for _, change := range request.Changes {
			if change.Operation == "delete" {
				if err := multipartWriter.WriteField("files", "/"+change.Path); err != nil {
					writeErr = err
					return
				}
				continue
			}
			part, err := multipartWriter.CreateFormFile("/"+change.Path, path.Base(change.Path))
			if err != nil {
				writeErr = err
				return
			}
			if _, err := part.Write(change.Content); err != nil {
				writeErr = err
				return
			}
		}
	}()
	requestHTTP, err := b.requester.newRequest(ctx, http.MethodPost, bitbucketRepoAPIPath(request.Repository)+"/src", pipeReader)
	if err != nil {
		pipeReader.Close()
		return ApplyCommitResult{}, err
	}
	requestHTTP.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, err := b.requester.client.Do(requestHTTP)
	if err != nil {
		pipeReader.Close()
		<-writeErrors
		return ApplyCommitResult{}, b.requester.sanitizeError(err)
	}
	defer response.Body.Close()
	writeErr := <-writeErrors
	if writeErr != nil {
		return ApplyCommitResult{}, writeErr
	}
	data, readErr := readBounded(response.Body, maxRemoteJSON)
	if readErr != nil {
		return ApplyCommitResult{}, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		remoteErr := &RemoteError{Kind: ForgeBitbucket, Status: response.StatusCode, Method: http.MethodPost, URL: sanitizeEndpoint(requestHTTP.URL.String()), Body: b.requester.redact(string(data))}
		var err error = remoteErr
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusConflict {
			err = confirmStaleHead(ctx, b, request.Repository, request.Branch, request.ExpectedHead, remoteErr)
		}
		return ApplyCommitResult{}, err
	}
	var commit bitbucketCommit
	if err := json.Unmarshal(data, &commit); err != nil {
		return ApplyCommitResult{}, fmt.Errorf("decode bitbucket commit response: %w", err)
	}
	if commit.Hash == "" {
		return ApplyCommitResult{}, errors.New("bitbucket returned an empty commit hash")
	}
	result := ApplyCommitResult{CommitID: commit.Hash, ConditionalRef: false}
	for _, parent := range commit.Parents {
		result.ParentIDs = append(result.ParentIDs, parent.Hash)
	}
	return result, nil
}

func (b *bitbucketForge) validateNext(next string) error {
	base, err := url.Parse(b.apiBase)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(next)
	if err != nil || !parsed.IsAbs() || !sameOrigin(base, parsed) {
		return errors.New("bitbucket pagination returned an untrusted next URL")
	}
	return nil
}

func bitbucketRepoAPIPath(ref RepositoryRef) string {
	return "/repositories/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}

func bitbucketBlobID(commit, filePath string) string { return commit + "\n" + filePath }

func parseBitbucketBlobID(value string) (string, string, error) {
	commit, filePath, found := strings.Cut(value, "\n")
	if !found || commit == "" {
		return "", "", errors.New("invalid bitbucket remote file identity")
	}
	cleaned, err := validateRemotePath(filePath)
	return commit, cleaned, err
}

func escapeRemotePath(filePath string) string {
	parts := strings.Split(filePath, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func parseBitbucketRepository(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	repositoryPath := value
	if strings.HasPrefix(strings.ToLower(repositoryPath), "bitbucket.org/") {
		repositoryPath = repositoryPath[len("bitbucket.org/"):]
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		if !strings.EqualFold(parsed.Hostname(), "bitbucket.org") {
			return "", "", errors.New("Bitbucket Cloud repository URL must use bitbucket.org")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", errors.New("bitbucket repository URL must not contain a query or fragment")
		}
		repositoryPath = strings.Trim(parsed.Path, "/")
	}
	parts := strings.Split(strings.Trim(repositoryPath, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("Bitbucket Cloud repository must be WORKSPACE/REPOSITORY")
	}
	name := strings.TrimSuffix(parts[1], ".git")
	if name == "" {
		return "", "", errors.New("Bitbucket Cloud repository name is empty")
	}
	return strings.Trim(parts[0], "{}"), strings.Trim(name, "{}"), nil
}

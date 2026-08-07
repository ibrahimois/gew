package forge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type contractForge struct {
	kind         ForgeKind
	capabilities ForgeCapabilities
	head         string
	result       ApplyCommitResult
	err          error
	calls        int
	request      ApplyCommitRequest
}

func (f *contractForge) Kind() ForgeKind { return f.kind }
func (f *contractForge) Capabilities() ForgeCapabilities {
	return f.capabilities
}
func (f *contractForge) Probe(context.Context) error { return nil }
func (f *contractForge) ResolveRepository(context.Context, string) (RepositoryRef, RepositoryInfo, error) {
	return RepositoryRef{Forge: f.kind}, RepositoryInfo{}, nil
}
func (f *contractForge) Head(context.Context, RepositoryRef, string) (string, error) {
	return f.head, nil
}
func (f *contractForge) Tree(context.Context, RepositoryRef, string) (map[string]RemoteFile, error) {
	return map[string]RemoteFile{}, nil
}
func (f *contractForge) Blob(context.Context, RepositoryRef, RemoteFile) ([]byte, error) {
	return nil, nil
}
func (f *contractForge) CommitDetails(context.Context, RepositoryRef, string) (RemoteCommit, error) {
	return RemoteCommit{}, f.err
}
func (f *contractForge) ApplyCommit(_ context.Context, request ApplyCommitRequest) (ApplyCommitResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func runForgeBaseContract(t *testing.T, remote Forge, kind ForgeKind, nativeSnapshot, writer, push bool) {
	t.Helper()
	if remote.Kind() != kind {
		t.Fatalf("Kind() = %q, want %q", remote.Kind(), kind)
	}
	if _, ok := any(remote).(RepositoryReader); !ok {
		t.Fatal("adapter does not implement RepositoryReader")
	}
	if _, ok := any(remote).(ForgeSnapshotter); ok != nativeSnapshot {
		t.Fatalf("ForgeSnapshotter = %v, want %v", ok, nativeSnapshot)
	}
	if _, ok := any(remote).(ForgeCommitWriter); ok != writer {
		t.Fatalf("ForgeCommitWriter = %v, want %v", ok, writer)
	}
	if remote.Capabilities().Push != push {
		t.Fatalf("Push = %v, want %v", remote.Capabilities().Push, push)
	}
	if remote.Capabilities().Push && !writer {
		t.Fatal("push-enabled adapter has no writer role")
	}
	if remote.Capabilities().BranchCreate && !writer {
		t.Fatal("branch creation requires a writer role")
	}
}

func TestForgeContractRemoteErrorClassification(t *testing.T) {
	if !errors.Is(&RemoteError{Status: 404}, ErrNotFound) {
		t.Fatal("404 must classify as not found")
	}
	for _, status := range []int{409, 412, 422} {
		if errors.Is(&RemoteError{Status: status}, ErrStaleHead) {
			t.Fatalf("generic status %d must not classify as stale head", status)
		}
	}
}

func TestForgeContractConfirmedStaleHeadPreservesProviderError(t *testing.T) {
	providerErr := &RemoteError{Kind: ForgeGitea, Status: 409, Method: "POST", URL: "/contents", Body: "conflict"}
	remote := &contractForge{head: "advanced"}
	err := ConfirmStaleHead(context.Background(), remote, RepositoryRef{}, "main", "base", providerErr)
	if !errors.Is(err, ErrStaleHead) {
		t.Fatalf("changed head error = %v", err)
	}
	var retained *RemoteError
	if !errors.As(err, &retained) || retained != providerErr {
		t.Fatalf("provider error was not retained: %v", err)
	}
	remote.head = "base"
	err = ConfirmStaleHead(context.Background(), remote, RepositoryRef{}, "main", "base", providerErr)
	if errors.Is(err, ErrStaleHead) || err != providerErr {
		t.Fatalf("unchanged head error = %v", err)
	}
}

func TestForgeContractValidatedWriterRequestInvariants(t *testing.T) {
	valid := ApplyCommitRequest{
		Repository: RepositoryRef{Forge: ForgeGitea}, Branch: " main ", ExpectedHead: "base", Message: " message ",
		Changes: []RemoteChange{{Operation: "update", Path: "dir/file.txt", Content: []byte("data")}},
	}
	tests := []struct {
		name   string
		mutate func(*ApplyCommitRequest)
	}{
		{name: "empty branch", mutate: func(r *ApplyCommitRequest) { r.Branch = " " }},
		{name: "empty message", mutate: func(r *ApplyCommitRequest) { r.Message = " " }},
		{name: "empty changes", mutate: func(r *ApplyCommitRequest) { r.Changes = nil }},
		{name: "invalid operation", mutate: func(r *ApplyCommitRequest) { r.Changes[0].Operation = "move" }},
		{name: "unsafe path", mutate: func(r *ApplyCommitRequest) { r.Changes[0].Path = "../escape" }},
		{name: "duplicate path", mutate: func(r *ApplyCommitRequest) {
			r.Changes = append(r.Changes, RemoteChange{Operation: "delete", Path: "dir/./file.txt"})
		}},
		{name: "provider mismatch", mutate: func(r *ApplyCommitRequest) { r.Repository.Forge = ForgeGitHub }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}, result: ApplyCommitResult{CommitID: "next", ConditionalRef: true}}
			writer, err := Writer(remote, false)
			if err != nil {
				t.Fatal(err)
			}
			request := valid
			request.Changes = append([]RemoteChange(nil), valid.Changes...)
			test.mutate(&request)
			if _, err := writer.ApplyCommit(context.Background(), request); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if remote.calls != 0 {
				t.Fatalf("raw writer called %d time(s)", remote.calls)
			}
		})
	}

	remote := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}, result: ApplyCommitResult{CommitID: "next", ConditionalRef: true}}
	writer, _ := Writer(remote, false)
	originalContent := append([]byte(nil), valid.Changes[0].Content...)
	if _, err := writer.ApplyCommit(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if remote.calls != 1 || remote.request.Branch != "main" || remote.request.Message != "message" {
		t.Fatalf("validated request = %#v, calls=%d", remote.request, remote.calls)
	}
	remote.request.Changes[0].Content[0] = 'X'
	if !reflect.DeepEqual(valid.Changes[0].Content, originalContent) {
		t.Fatal("validated writer mutated caller-owned content")
	}
}

func TestForgeContractValidatedWriterResultInvariants(t *testing.T) {
	tests := []struct {
		name    string
		result  ApplyCommitResult
		wantErr bool
	}{
		{name: "conditional success", result: ApplyCommitResult{CommitID: "next", ConditionalRef: true}},
		{name: "matching parent", result: ApplyCommitResult{CommitID: "next", ParentIDs: []string{"base"}}},
		{name: "missing id", result: ApplyCommitResult{ConditionalRef: true}, wantErr: true},
		{name: "missing parent", result: ApplyCommitResult{CommitID: "next"}, wantErr: true},
		{name: "unexpected parent", result: ApplyCommitResult{CommitID: "next", ParentIDs: []string{"other"}}, wantErr: true},
	}
	request := ApplyCommitRequest{Repository: RepositoryRef{Forge: ForgeGitea}, Branch: "main", ExpectedHead: "base", Message: "message", Changes: []RemoteChange{{Operation: "create", Path: "file"}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}, result: test.result}
			writer, _ := Writer(remote, false)
			_, err := writer.ApplyCommit(context.Background(), request)
			if (err != nil) != test.wantErr {
				t.Fatalf("ApplyCommit() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}

	initial := request
	initial.ExpectedHead = ""
	remote := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}, result: ApplyCommitResult{CommitID: "initial"}}
	writer, _ := Writer(remote, false)
	if _, err := writer.ApplyCommit(context.Background(), initial); err != nil {
		t.Fatalf("initial commit without parents: %v", err)
	}
}

func TestForgeContractWriterCapabilityGatesAndErrorIdentity(t *testing.T) {
	disabled := &contractForge{kind: ForgeGitLab}
	if _, err := Writer(disabled, false); !errors.Is(err, ErrUnsupported) || disabled.calls != 0 {
		t.Fatalf("disabled writer error = %v, calls=%d", err, disabled.calls)
	}
	noBranch := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}}
	if _, err := Writer(noBranch, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("branch gate error = %v", err)
	}

	sentinel := errors.New("provider rejected mutation")
	failing := &contractForge{kind: ForgeGitea, capabilities: ForgeCapabilities{Push: true}, err: sentinel}
	writer, _ := Writer(failing, false)
	request := ApplyCommitRequest{Branch: "main", Message: "message", Changes: []RemoteChange{{Operation: "create", Path: "file"}}}
	_, err := writer.ApplyCommit(context.Background(), request)
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), "stale") {
		t.Fatalf("adapter error classification changed: %v", err)
	}
}

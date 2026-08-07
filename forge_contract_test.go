package main

import (
	"context"
	"errors"
	"testing"
)

func TestForgeContractApplyParentInvariant(t *testing.T) {
	request := ApplyCommitRequest{ExpectedHead: "base"}
	tests := []struct {
		name    string
		result  ApplyCommitResult
		wantErr bool
	}{
		{name: "conditional success", result: ApplyCommitResult{CommitID: "next", ConditionalRef: true}},
		{name: "matching nonconditional parent", result: ApplyCommitResult{CommitID: "next", ParentIDs: []string{"base"}}},
		{name: "unexpected nonconditional parent", result: ApplyCommitResult{CommitID: "next", ParentIDs: []string{"other"}}, wantErr: true},
		{name: "missing commit id", result: ApplyCommitResult{ConditionalRef: true}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateApplyResult(request, test.result)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateApplyResult() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestForgeContractInitialCommitParentInvariant(t *testing.T) {
	request := ApplyCommitRequest{}
	if err := validateApplyResult(request, ApplyCommitResult{CommitID: "initial"}); err != nil {
		t.Fatalf("initial commit without parents: %v", err)
	}
	if err := validateApplyResult(request, ApplyCommitResult{CommitID: "initial", ParentIDs: []string{"unexpected"}}); err == nil {
		t.Fatal("initial commit with a parent was accepted")
	}
}

func TestForgeContractRemoteErrorClassification(t *testing.T) {
	if !errors.Is(&RemoteError{Status: 404}, ErrNotFound) {
		t.Fatal("404 must classify as not found")
	}
	for _, status := range []int{409, 412, 422} {
		if !errors.Is(&RemoteError{Status: status}, ErrStaleHead) {
			t.Fatalf("status %d must classify as stale head", status)
		}
	}
}

type contractForge struct {
	Forge
	result ApplyCommitResult
	err    error
}

func (f contractForge) Kind() ForgeKind { return ForgeKind("contract") }
func (f contractForge) Capabilities() ForgeCapabilities {
	return ForgeCapabilities{AtomicMultiFile: true, Push: true}
}
func (f contractForge) ApplyCommit(context.Context, ApplyCommitRequest) (ApplyCommitResult, error) {
	return f.result, f.err
}

func TestForgeContractStaleHeadPreservesMutationBoundary(t *testing.T) {
	forge := contractForge{err: ErrStaleHead}
	_, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{ExpectedHead: "base"})
	if !errors.Is(err, ErrStaleHead) {
		t.Fatalf("ApplyCommit() error = %v", err)
	}
}

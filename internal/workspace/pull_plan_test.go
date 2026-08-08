package workspace

import (
	"reflect"
	"testing"

	"gew/internal/forge"
)

func TestBuildPullPlanDeterministicDelta(t *testing.T) {
	base := Manifest{
		"delete.txt": {BlobSHA: "delete", Hash: "old", Mode: 0o644},
		"mode.sh":    {BlobSHA: "same", Hash: "mode", Mode: 0o644},
		"modify.txt": {BlobSHA: "old", Hash: "old", Mode: 0o644},
	}
	remote := map[string]forge.RemoteFile{
		"create.txt": {BlobID: "create", Mode: 0o100644},
		"mode.sh":    {BlobID: "same", Mode: 0o100755},
		"modify.txt": {BlobID: "new", Mode: 0o100644},
	}
	plan, err := BuildPullPlan(base, remote)
	if err != nil {
		t.Fatal(err)
	}
	want := []PullOperationKind{PullCreate, PullDelete, PullMode, PullModify}
	got := make([]PullOperationKind, len(plan.Operations))
	for index, operation := range plan.Operations {
		got[index] = operation.Kind
	}
	if !reflect.DeepEqual(got, want) || len(plan.Downloads) != 2 {
		t.Fatalf("operations=%v downloads=%v", got, plan.Downloads)
	}
}

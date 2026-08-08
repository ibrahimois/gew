package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSyncProgressModesAndTimingSummary(t *testing.T) {
	var output bytes.Buffer
	observer, err := newSyncObserver(&output, syncOptions{Progress: "always", Timings: true})
	if err != nil {
		t.Fatal(err)
	}
	done := observer.phase("head")
	done()
	observer.add(2, 12)
	if err := observer.finish(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"head...", "Sync timings:", "files=2", "bytes=12", "head="} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q: %s", expected, text)
		}
	}
	if _, err := newSyncObserver(&output, syncOptions{Progress: "sometimes"}); err == nil {
		t.Fatal("invalid progress mode accepted")
	}
}

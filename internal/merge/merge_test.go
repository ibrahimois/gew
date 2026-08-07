package merge

import (
	"bytes"
	"testing"
)

func TestText(t *testing.T) {
	merged, conflict := Text(
		[]byte("one\ntwo\nthree\n"),
		[]byte("ONE\ntwo\nthree\n"),
		[]byte("one\ntwo\nTHREE\n"),
	)
	if conflict || string(merged) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("non-overlapping merge = %q, conflict=%v", merged, conflict)
	}

	merged, conflict = Text([]byte("base\n"), []byte("ours\n"), []byte("theirs\n"))
	if !conflict || !bytes.Contains(merged, []byte("<<<<<<< ours\n")) {
		t.Fatalf("overlapping merge = %q, conflict=%v", merged, conflict)
	}
}

func TestFile(t *testing.T) {
	text := func(value string) Content { return Content{Exists: true, Content: []byte(value), Mode: 0o644} }
	if merged, conflict, _ := File(text("base\n"), Content{}, text("base\n")); conflict || merged.Exists {
		t.Fatalf("delete against unchanged content = %#v, conflict=%v", merged, conflict)
	}
	if _, conflict, binary := File(
		Content{Exists: true, Content: []byte{0, 1}},
		Content{Exists: true, Content: []byte{0, 2}},
		Content{Exists: true, Content: []byte{0, 3}},
	); !conflict || !binary {
		t.Fatalf("binary merge conflict=%v, binary=%v", conflict, binary)
	}
}

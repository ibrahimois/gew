package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkScanWorkspace(b *testing.B) {
	shapes := []struct {
		name  string
		files int
		size  int
	}{
		{name: "medium-1000", files: 1_000, size: 4096},
		{name: "tiny-20000", files: 20_000, size: 16},
	}
	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			root := b.TempDir()
			content := make([]byte, shape.size)
			for index := 0; index < shape.files; index++ {
				name := filepath.Join(root, fmt.Sprintf("dir-%03d", index/100), fmt.Sprintf("file-%05d", index))
				if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(name, content, 0o644); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if _, err := scanWorkspace(root); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

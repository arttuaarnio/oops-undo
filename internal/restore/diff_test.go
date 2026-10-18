package restore

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDiff(t *testing.T) {
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	mtime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{oldRoot, newRoot} {
		write(t, root, "same.txt", "same")
		write(t, root, "sneaky.txt", "aaaa")
		write(t, root, "mode.sh", "x")
		for _, p := range []string{"same.txt", "sneaky.txt"} {
			if err := os.Chtimes(filepath.Join(root, p), mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(t, oldRoot, "mod.txt", "old")
	write(t, newRoot, "mod.txt", "new content")
	write(t, oldRoot, "src/legacy/a.go", "a")
	write(t, oldRoot, "src/legacy/b.go", "b")
	write(t, oldRoot, "src/main.go", "main")
	write(t, newRoot, "src/main.go", "main")
	if err := os.Chtimes(filepath.Join(newRoot, "src/main.go"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	write(t, newRoot, "build/out.bin", "x")
	if err := os.Chmod(filepath.Join(newRoot, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Same size and mtime, different content: only --exact sees it.
	write(t, newRoot, "sneaky.txt", "bbbb")
	if err := os.Chtimes(filepath.Join(newRoot, "sneaky.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	// src/main.go has the same content but an older mtime on the new side.
	want := []Change{
		{'A', "build/"},
		{'M', "mod.txt"},
		{'M', "mode.sh"},
		{'D', "src/legacy/"},
		{'M', "src/main.go"},
	}
	got, err := Diff(oldRoot, newRoot, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Diff:\n got %q\nwant %q", got, want)
	}

	got, err = Diff(oldRoot, newRoot, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, Change{'M', "sneaky.txt"}) {
		t.Errorf("exact diff should catch the content change: %q", got)
	}

	got, err = Diff(oldRoot, newRoot, "src", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Change{{'D', "src/legacy/"}, {'M', "src/main.go"}}; !slices.Equal(got, want) {
		t.Errorf("Diff limited to src:\n got %q\nwant %q", got, want)
	}
}

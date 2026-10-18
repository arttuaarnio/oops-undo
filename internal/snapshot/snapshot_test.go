package snapshot

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/arttuaarnio/oops-undo/internal/store"
)

func TestTakeKeepsContentModesSymlinksAndMtimes(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	folder := t.TempDir()
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	mustWrite(t, filepath.Join(folder, "src", "main.go"), "package main", 0o644)
	mustWrite(t, filepath.Join(folder, "run.sh"), "#!/bin/sh", 0o755)
	mustWrite(t, filepath.Join(folder, ".gitignored"), "secret", 0o600)
	if err := os.Chtimes(filepath.Join(folder, "run.sh"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/main.go", filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}

	snap := store.New(folder, store.KindManual)
	if err := Take(st, snap); err != nil {
		t.Fatal(err)
	}
	data := st.DataDir(snap.ID)

	if got := mustRead(t, filepath.Join(data, "src", "main.go")); got != "package main" {
		t.Errorf("content = %q", got)
	}
	if got := mustRead(t, filepath.Join(data, ".gitignored")); got != "secret" {
		t.Errorf("hidden file content = %q", got)
	}
	info, err := os.Stat(filepath.Join(data, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	if !info.ModTime().Equal(mtime) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), mtime)
	}
	if target, err := os.Readlink(filepath.Join(data, "link")); err != nil || target != "src/main.go" {
		t.Errorf("symlink = %q, %v; want it kept as a link to src/main.go", target, err)
	}
	// src, src/main.go, run.sh, .gitignored, link
	if snap.FileCount != 5 {
		t.Errorf("FileCount = %d, want 5", snap.FileCount)
	}
	if _, err := st.Load(snap.ID); err != nil {
		t.Errorf("metadata not saved: %v", err)
	}
}

func TestSnapshotIsIndependentOfLaterChanges(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	folder := t.TempDir()
	mustWrite(t, filepath.Join(folder, "a.txt"), "before", 0o644)

	snap := store.New(folder, store.KindManual)
	if err := Take(st, snap); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(folder, "a.txt"), "after", 0o644)

	if got := mustRead(t, filepath.Join(st.DataDir(snap.ID), "a.txt")); got != "before" {
		t.Errorf("snapshot changed with the folder: %q", got)
	}
}

func TestTakeFailureLeavesNoSnapshot(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	snap := store.New(filepath.Join(t.TempDir(), "missing"), store.KindManual)
	if err := Take(st, snap); err == nil {
		t.Fatal("snapshot of a missing folder should fail")
	}
	if _, err := os.Stat(st.SnapshotDir(snap.ID)); !os.IsNotExist(err) {
		t.Errorf("partial snapshot left behind: %v", err)
	}
}

func BenchmarkTake(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(strconv.Itoa(n)+"_files", func(b *testing.B) {
			st := &store.Store{Root: b.TempDir()}
			folder := b.TempDir()
			for i := range n {
				dir := filepath.Join(folder, "d"+strconv.Itoa(i%20))
				mustWrite(b, filepath.Join(dir, "f"+strconv.Itoa(i)), "x", 0o644)
			}
			for b.Loop() {
				if err := Take(st, store.New(folder, store.KindManual)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func mustWrite(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile's mode is masked by umask
		t.Fatal(err)
	}
}

func mustRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

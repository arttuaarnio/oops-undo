package restore

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/arttuaarnio/oops-undo/internal/snapshot"
	"github.com/arttuaarnio/oops-undo/internal/store"
)

// setup creates a project folder, snapshots it, then changes it: one file
// modified, one deleted, a folder deleted, a file and a folder added.
// It returns the store, the snapshot, and the folder's state before and
// after the changes.
func setup(t *testing.T) (st *store.Store, snap *store.Snapshot, before, after map[string]string) {
	t.Helper()
	st = &store.Store{Root: t.TempDir()}
	folder := t.TempDir()
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	write(t, folder, "keep.txt", "keep")
	write(t, folder, "mod.txt", "original")
	write(t, folder, "del.txt", "delete me")
	write(t, folder, "src/legacy/old.go", "package legacy")
	write(t, folder, "src/main.go", "package main")
	write(t, folder, ".git/HEAD", "ref: main")
	if err := os.Chmod(filepath.Join(folder, "keep.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(folder, "keep.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/main.go", filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}

	snap = store.New(folder, store.KindManual)
	if err := snapshot.Take(st, snap); err != nil {
		t.Fatal(err)
	}
	before = tree(t, folder)

	write(t, folder, "mod.txt", "changed")
	write(t, folder, ".git/HEAD", "ref: other")
	write(t, folder, "new.txt", "new")
	write(t, folder, "build/out.bin", "binary")
	for _, p := range []string{"del.txt", "src/legacy", "link"} {
		if err := os.RemoveAll(filepath.Join(folder, p)); err != nil {
			t.Fatal(err)
		}
	}
	after = tree(t, folder)
	return st, snap, before, after
}

func TestRestoreWholeFolder(t *testing.T) {
	st, snap, before, _ := setup(t)
	folderIno := mustInode(t, snap.Folder)

	if _, err := Run(st, snap, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, snap.Folder); !maps.Equal(got, before) {
		t.Errorf("folder after restore:\n%v\nwant:\n%v", got, before)
	}
	if mustInode(t, snap.Folder) != folderIno {
		t.Error("the folder itself was replaced; a shell inside it would lose its working directory")
	}
	assertNoLeftovers(t, st)
}

func TestRestoreSinglePath(t *testing.T) {
	st, snap, before, after := setup(t)

	if _, err := Run(st, snap, Options{Path: "src/legacy"}); err != nil {
		t.Fatal(err)
	}
	want := maps.Clone(after)
	want["src/legacy"] = before["src/legacy"]
	want["src/legacy/old.go"] = before["src/legacy/old.go"]
	if got := tree(t, snap.Folder); !maps.Equal(got, want) {
		t.Errorf("only src/legacy should come back:\n%v\nwant:\n%v", got, want)
	}
	assertNoLeftovers(t, st)
}

func TestRestoreNoGit(t *testing.T) {
	st, snap, _, after := setup(t)

	if _, err := Run(st, snap, Options{NoGit: true}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, snap.Folder); got[".git/HEAD"] != after[".git/HEAD"] {
		t.Errorf(".git was restored: %q", got[".git/HEAD"])
	}
}

func TestRestoreDeletedFolder(t *testing.T) {
	st, snap, before, _ := setup(t)
	if err := os.RemoveAll(snap.Folder); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(st, snap, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, snap.Folder); !maps.Equal(got, before) {
		t.Errorf("deleted folder not restored:\n%v", got)
	}
}

func TestRestoreCanBeUndone(t *testing.T) {
	st, snap, _, after := setup(t)

	pre, err := Run(st, snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(st, pre, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, snap.Folder); !maps.Equal(got, after) {
		t.Errorf("undoing the restore should bring back the changed state:\n%v\nwant:\n%v", got, after)
	}
}

func TestFailedRestoreRollsBack(t *testing.T) {
	st, snap, _, after := setup(t)
	// A locked (uchg) file can't be renamed, so the restore fails on
	// src/, after the earlier entries were already swapped.
	locked := filepath.Join(snap.Folder, "src")
	if err := unix.Chflags(locked, unix.UF_IMMUTABLE); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unlockAll(t, snap.Folder, st.Root) })

	_, err := Run(st, snap, Options{})
	if err == nil {
		t.Fatal("restore should fail on a locked entry")
	}
	t.Logf("restore failed as expected: %v", err)
	if got := tree(t, snap.Folder); !maps.Equal(got, after) {
		t.Errorf("a failed restore must leave the folder as it was:\n%v\nwant:\n%v", got, after)
	}
	if ids, _ := Pending(st); len(ids) != 0 {
		t.Errorf("journal left after a successful rollback: %v", ids)
	}
}

func TestInterruptedRestoreRollsBack(t *testing.T) {
	st, snap, _, after := setup(t)

	// Simulate a crash: write the journal, run half the steps, stop.
	steps, err := plan(st.DataDir(snap.ID), snap.Folder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	j := &journal{RestoreID: store.NewID(time.Now()), SnapshotID: snap.ID, Folder: snap.Folder, Steps: steps}
	if err := os.MkdirAll(st.TrashDir(j.RestoreID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(journalPath(st, j.RestoreID), j); err != nil {
		t.Fatal(err)
	}
	for i := range len(steps) / 2 {
		if err := apply(st, j, i, steps[i]); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Run(st, snap, Options{}); !errors.Is(err, ErrPending) {
		t.Fatalf("a new restore should refuse while one is pending, got %v", err)
	}
	folders, err := RollbackPending(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0] != snap.Folder {
		t.Errorf("rolled back folders = %v", folders)
	}
	if got := tree(t, snap.Folder); !maps.Equal(got, after) {
		t.Errorf("rollback should bring back the state before the restore:\n%v\nwant:\n%v", got, after)
	}
	assertNoLeftovers(t, st)
}

func TestRestoreRejectsPathOutsideFolder(t *testing.T) {
	st, snap, _, _ := setup(t)
	for _, p := range []string{"../escape", "/etc", "src/../../x"} {
		if _, err := Run(st, snap, Options{Path: p}); err == nil {
			t.Errorf("path %q should be rejected", p)
		}
	}
}

// tree describes every entry under root as type, mode, mtime (for files)
// and content or link target, keyed by relative path.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			out[rel] = "link " + target
			return err
		case info.IsDir():
			out[rel] = "dir " + info.Mode().String()
		default:
			b, err := os.ReadFile(path)
			out[rel] = "file " + info.Mode().String() + " " + info.ModTime().UTC().String() + " " + string(b)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustInode(t *testing.T, path string) uint64 {
	t.Helper()
	ino, err := inode(path)
	if err != nil || ino == 0 {
		t.Fatalf("inode(%s) = %d, %v", path, ino, err)
	}
	return ino
}

func assertNoLeftovers(t *testing.T, st *store.Store) {
	t.Helper()
	for _, dir := range []string{st.RestoresDir(), filepath.Join(st.Root, "trash")} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("%s should be empty, has %d entries", dir, len(entries))
		}
	}
}

// unlockAll clears uchg flags so the test's temp folders can be deleted.
func unlockAll(t *testing.T, dirs ...string) {
	for _, dir := range dirs {
		if out, err := exec.Command("chflags", "-R", "nouchg", dir).CombinedOutput(); err != nil {
			t.Logf("chflags %s: %v %s", dir, err, out)
		}
	}
}

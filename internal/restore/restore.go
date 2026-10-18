// Package restore puts a folder back to the state saved in a snapshot, and
// compares snapshots with the folder's current state.
package restore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/arttuaarnio/oops-undo/internal/snapshot"
	"github.com/arttuaarnio/oops-undo/internal/store"
)

// ErrPending means an earlier restore was interrupted and must be rolled
// back before another restore can run.
var ErrPending = errors.New("an earlier restore was interrupted")

// Options choose what to restore.
type Options struct {
	Path  string // restore only this path, relative to the folder; empty restores everything
	NoGit bool   // leave .git untouched in a whole-folder restore
}

// journal records a restore before it starts, so that an interrupted
// restore can be rolled back later, even after a crash.
type journal struct {
	RestoreID  string `json:"restore_id"`
	SnapshotID string `json:"snapshot_id"`
	Folder     string `json:"folder"`
	Steps      []step `json:"steps"`
}

// step restores one entry: a top-level entry, or the single path asked for.
type step struct {
	Name       string `json:"name"`        // relative to the folder
	InSnapshot bool   `json:"in_snapshot"` // false: the entry is new since the snapshot and gets removed
	// OldIno is the inode number of the live entry before the restore, or 0
	// if it didn't exist. Every step changes which inode sits at the entry's
	// path, so comparing with OldIno tells whether the step has run, without
	// writing to the journal after each step.
	OldIno uint64 `json:"old_ino"`
}

// Run restores snap into its folder. It first takes a pre-restore snapshot,
// so the restore itself can be undone, and returns it. If any step fails,
// every step done so far is undone before Run returns.
func Run(st *store.Store, snap *store.Snapshot, opt Options) (*store.Snapshot, error) {
	pending, err := Pending(st)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, ErrPending
	}
	if opt.Path != "" && !filepath.IsLocal(opt.Path) {
		return nil, fmt.Errorf("path %q must be relative and stay inside the folder", opt.Path)
	}
	// The folder may have been deleted entirely; recreate it so it can be filled.
	if err := os.MkdirAll(snap.Folder, 0o755); err != nil {
		return nil, err
	}

	pre := store.New(snap.Folder, store.KindPreRestore)
	pre.Message = "before restoring " + snap.ID[:10]
	if err := snapshot.Take(st, pre); err != nil {
		return nil, fmt.Errorf("could not save the current state, so nothing was changed: %w", err)
	}

	steps, err := plan(st.DataDir(snap.ID), snap.Folder, opt)
	if err != nil {
		return pre, err
	}
	j := &journal{RestoreID: store.NewID(time.Now()), SnapshotID: snap.ID, Folder: snap.Folder, Steps: steps}
	if err := os.MkdirAll(st.TrashDir(j.RestoreID), 0o700); err != nil {
		return pre, err
	}
	if err := store.WriteJSON(journalPath(st, j.RestoreID), j); err != nil {
		return pre, err
	}

	for i, s := range j.Steps {
		if err := apply(st, j, i, s); err != nil {
			if rbErr := rollback(st, j); rbErr != nil {
				return pre, fmt.Errorf("restore failed (%w) and undoing it also failed (%v); the journal is kept in %s",
					err, rbErr, filepath.Dir(journalPath(st, j.RestoreID)))
			}
			return pre, fmt.Errorf("restore failed and every change was undone: %w", err)
		}
	}
	finish(st, j.RestoreID)
	return pre, nil
}

// plan lists the steps for a restore and records each live entry's inode.
func plan(data, folder string, opt Options) ([]step, error) {
	names := []string{opt.Path}
	if opt.Path == "" {
		var err error
		if names, err = topLevelNames(data, folder, opt.NoGit); err != nil {
			return nil, err
		}
	}
	var steps []step
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(data, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		inSnapshot := err == nil
		ino, err := inode(filepath.Join(folder, name))
		if err != nil {
			return nil, err
		}
		if !inSnapshot && ino == 0 {
			return nil, fmt.Errorf("%s is neither in the snapshot nor in the folder", name)
		}
		steps = append(steps, step{Name: name, InSnapshot: inSnapshot, OldIno: ino})
	}
	return steps, nil
}

// topLevelNames returns the names found in either folder, sorted.
func topLevelNames(data, folder string, noGit bool) ([]string, error) {
	seen := map[string]bool{}
	for _, dir := range []string{data, folder} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !(noGit && e.Name() == ".git") {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// apply runs one step. The snapshot copy is first cloned into the trash
// folder (same volume, so renames between them work), then moved into the
// folder in a single rename, so the entry is never missing or half-written.
func apply(st *store.Store, j *journal, i int, s step) error {
	live := filepath.Join(j.Folder, s.Name)
	parked := parkedPath(st, j.RestoreID, i)

	if !s.InSnapshot {
		return os.Rename(live, parked)
	}
	if err := snapshot.Clone(filepath.Join(st.DataDir(j.SnapshotID), s.Name), parked); err != nil {
		return err
	}
	if s.OldIno == 0 {
		// Restoring a single path may need its parent folders recreated.
		if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			return err
		}
		return renamex(parked, live, unix.RENAME_EXCL)
	}
	// RENAME_SWAP exchanges the two entries in one atomic step: afterwards
	// live holds the snapshot copy and parked holds the old content.
	return renamex(parked, live, unix.RENAME_SWAP)
}

// rollback undoes every step that has run, newest first. Each undo checks
// the live entry's inode, so running rollback twice is safe.
func rollback(st *store.Store, j *journal) error {
	var errs []error
	for i := len(j.Steps) - 1; i >= 0; i-- {
		if err := undo(st, j, i, j.Steps[i]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", j.Steps[i].Name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	finish(st, j.RestoreID)
	return nil
}

func undo(st *store.Store, j *journal, i int, s step) error {
	live := filepath.Join(j.Folder, s.Name)
	parked := parkedPath(st, j.RestoreID, i)
	cur, err := inode(live)
	if err != nil {
		return err
	}
	switch {
	case cur == s.OldIno: // the step never ran
		return nil
	case s.OldIno == 0: // the entry was added: move it back out
		return os.Rename(live, parked)
	case cur == 0: // the entry was removed: put it back
		return os.Rename(parked, live)
	default: // the entry was swapped: swap it back
		return renamex(parked, live, unix.RENAME_SWAP)
	}
}

// finish deletes the journal first, then the trash. Once the journal is
// gone nothing will try to roll back, so a crash in between only leaves
// harmless old files in the trash.
func finish(st *store.Store, restoreID string) {
	_ = os.RemoveAll(filepath.Dir(journalPath(st, restoreID)))
	// A locked (uchg) file can make this fail; leftover trash is harmless.
	_ = os.RemoveAll(st.TrashDir(restoreID))
}

// Pending returns the IDs of restores that were interrupted.
func Pending(st *store.Store) ([]string, error) {
	entries, err := os.ReadDir(st.RestoresDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if _, err := os.Stat(journalPath(st, e.Name())); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// RollbackPending undoes every interrupted restore and returns their folders.
func RollbackPending(st *store.Store) ([]string, error) {
	ids, err := Pending(st)
	if err != nil {
		return nil, err
	}
	var folders []string
	for _, id := range ids {
		var j journal
		if err := store.ReadJSON(journalPath(st, id), &j); err != nil {
			return folders, err
		}
		if err := rollback(st, &j); err != nil {
			return folders, err
		}
		folders = append(folders, j.Folder)
	}
	return folders, nil
}

func journalPath(st *store.Store, restoreID string) string {
	return filepath.Join(st.RestoresDir(), restoreID, "journal.json")
}

// parkedPath is where step i keeps the entry that is not in the folder:
// first the snapshot copy waiting to go in, then the old content after it.
func parkedPath(st *store.Store, restoreID string, i int) string {
	return filepath.Join(st.TrashDir(restoreID), strconv.Itoa(i))
}

// inode returns the inode number of path, or 0 if it doesn't exist.
func inode(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Sys().(*syscall.Stat_t).Ino, nil
}

func renamex(from, to string, flag uint32) error {
	if err := unix.RenamexNp(from, to, flag); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

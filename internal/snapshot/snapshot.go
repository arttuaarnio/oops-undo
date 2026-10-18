// Package snapshot takes APFS copy-on-write snapshots of a folder.
package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/arttuaarnio/oops-undo/internal/store"
)

// ErrNotClonable means the folder can't be cloned into the store: it is on
// another volume or the volume is not APFS.
var ErrNotClonable = errors.New("folder is not on the same APFS volume as the oops store")

// Take clones every top-level entry of snap.Folder into the store, then
// writes snap's metadata. On failure it deletes the partial snapshot, so
// the store never holds half a snapshot.
func Take(st *store.Store, snap *store.Snapshot) error {
	if err := CheckVolume(snap.Folder, st.Root); err != nil {
		return err
	}
	data := st.DataDir(snap.ID)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	start := time.Now()
	if err := cloneEntries(snap.Folder, data); err != nil {
		_ = st.Delete(snap.ID)
		return err
	}
	snap.DurationMS = time.Since(start).Milliseconds()

	count, err := countFiles(data)
	if err != nil {
		_ = st.Delete(snap.ID)
		return err
	}
	snap.FileCount = count
	if err := st.Save(snap); err != nil {
		_ = st.Delete(snap.ID)
		return err
	}
	return nil
}

// cloneEntries clones each top-level entry separately instead of cloning the
// whole folder in one call. Apple discourages cloning big directories at
// once, because the kernel blocks every write to the source tree until the
// clone finishes. One entry at a time keeps each of those pauses short.
func cloneEntries(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := Clone(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Clone makes a copy-on-write clone of src (a file, symlink or whole
// folder) at dst, which must not exist yet. The clone shares disk blocks
// with src until either side changes.
func Clone(src, dst string) error {
	// CLONE_NOFOLLOW clones a symlink itself instead of the file it points to.
	err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
	if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("%w: %s", ErrNotClonable, src)
	}
	if err != nil {
		return &fs.PathError{Op: "clone", Path: src, Err: err}
	}
	return nil
}

// CheckVolume returns ErrNotClonable unless folder and store are on the
// same APFS volume. Clone also catches this, but checking first gives a
// clear error before any work is done.
func CheckVolume(folder, storeRoot string) error {
	var f, s unix.Statfs_t
	if err := unix.Statfs(folder, &f); err != nil {
		return &fs.PathError{Op: "statfs", Path: folder, Err: err}
	}
	if err := unix.Statfs(storeRoot, &s); err != nil {
		return &fs.PathError{Op: "statfs", Path: storeRoot, Err: err}
	}
	fsType := string(bytes.TrimRight(f.Fstypename[:], "\x00"))
	if fsType != "apfs" || f.Fsid != s.Fsid {
		return fmt.Errorf("%w: %s", ErrNotClonable, folder)
	}
	return nil
}

// countFiles counts every file, folder and symlink under root.
func countFiles(root string) (int, error) {
	count := -1 // don't count root itself
	err := filepath.WalkDir(root, func(_ string, _ fs.DirEntry, err error) error {
		count++
		return err
	})
	return count, err
}

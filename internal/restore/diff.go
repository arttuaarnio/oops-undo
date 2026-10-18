package restore

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Change is one difference between a snapshot and the folder now.
type Change struct {
	Op   byte   // 'A' added since the snapshot, 'M' modified, 'D' deleted
	Path string // relative to the folder; folders end in "/"
}

// Diff compares the snapshot data in oldRoot with the folder in newRoot.
// With sub set, only that relative path is compared. Size, modification
// time, mode and file type are compared; exact also compares file content.
func Diff(oldRoot, newRoot, sub string, exact bool) ([]Change, error) {
	before, err := scan(oldRoot, sub)
	if err != nil {
		return nil, err
	}
	after, err := scan(newRoot, sub)
	if err != nil {
		return nil, err
	}

	var changes []Change
	for path, old := range before {
		cur, ok := after[path]
		switch {
		case !ok:
			// Inside a deleted folder, report only the folder itself.
			if !inside(path, before, after) {
				changes = append(changes, Change{'D', display(path, old)})
			}
		case modified(old, cur, filepath.Join(oldRoot, path), filepath.Join(newRoot, path), exact):
			changes = append(changes, Change{'M', display(path, cur)})
		}
	}
	for path, cur := range after {
		if _, ok := before[path]; !ok && !inside(path, after, before) {
			changes = append(changes, Change{'A', display(path, cur)})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// scan returns every path under root/sub, relative to root, with its info.
// A missing root/sub is an empty tree, not an error.
func scan(root, sub string) (map[string]fs.FileInfo, error) {
	tree := map[string]fs.FileInfo{}
	err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		tree[rel] = info
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]fs.FileInfo{}, nil
	}
	return tree, err
}

// inside reports whether path's parent folder is also only on one side,
// meaning the parent is already reported as added or deleted.
func inside(path string, here, there map[string]fs.FileInfo) bool {
	parent := filepath.Dir(path)
	_, inHere := here[parent]
	_, inThere := there[parent]
	return parent != "." && inHere && !inThere
}

func modified(old, cur fs.FileInfo, oldPath, curPath string, exact bool) bool {
	if old.Mode() != cur.Mode() { // includes file type and permissions
		return true
	}
	// A folder's mtime changes whenever anything inside it changes, and
	// those changes are reported for the children, so skip it here.
	if old.IsDir() {
		return false
	}
	if old.Mode()&fs.ModeSymlink != 0 {
		a, _ := os.Readlink(oldPath)
		b, _ := os.Readlink(curPath)
		return a != b
	}
	if old.Size() != cur.Size() || !old.ModTime().Equal(cur.ModTime()) {
		return true
	}
	return exact && !sameContent(oldPath, curPath)
}

// sameContent compares two files chunk by chunk without loading them whole.
func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()

	bufA := make([]byte, 64*1024)
	bufB := make([]byte, 64*1024)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false
		}
		if errA != nil || errB != nil { // end of file (or a read error) on either side
			return errA == errB
		}
	}
}

func display(path string, info fs.FileInfo) string {
	if info.IsDir() {
		return path + "/"
	}
	return path
}

// Package safety refuses folders that oops must never snapshot or restore.
package safety

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Check returns an error if folder is too dangerous to snapshot or restore.
// All three paths must be absolute with symlinks resolved.
func Check(folder, home, store string) error {
	// macOS volumes are case-insensitive by default, so /users is /Users.
	// Comparing lowercase can only refuse more, never less.
	f := strings.ToLower(filepath.Clean(folder))
	h := strings.ToLower(filepath.Clean(home))
	s := strings.ToLower(filepath.Clean(store))

	for _, refused := range []string{"/", "/system", "/users", h} {
		if f == refused {
			return fmt.Errorf("%s is a system or home folder", folder)
		}
	}
	if within(f, s) {
		return fmt.Errorf("%s is inside the oops store", folder)
	}
	if within(s, f) {
		return fmt.Errorf("%s contains the oops store (%s)", folder, store)
	}
	return nil
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

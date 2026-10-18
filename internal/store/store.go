// Package store manages the ~/.oops folder: snapshot IDs, metadata files
// and finding snapshots by ID prefix.
package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Kind says why a snapshot was taken.
type Kind string

const (
	KindRun        Kind = "run"
	KindManual     Kind = "manual"
	KindPreRestore Kind = "pre-restore"
	KindHook       Kind = "hook"
)

// formatVersion is written into every meta.json so the format can change later.
const formatVersion = 1

// MinPrefix is the shortest ID prefix accepted on the command line.
const MinPrefix = 4

// Snapshot is the content of one meta.json file.
type Snapshot struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	Folder     string    `json:"folder"`
	Kind       Kind      `json:"kind"`
	Command    string    `json:"command,omitempty"`
	Message    string    `json:"message,omitempty"`
	ExitCode   *int      `json:"exit_code"` // nil until the wrapped command finishes
	DurationMS int64     `json:"duration_ms"`
	FileCount  int       `json:"file_count"`
}

// Store is the root folder that holds all snapshots, usually ~/.oops.
type Store struct {
	Root string
}

// Default returns the store in the user's home folder.
func Default() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{Root: filepath.Join(home, ".oops")}, nil
}

// Init creates the store's folders if they don't exist yet.
func (s *Store) Init() error {
	for _, dir := range []string{"snapshots", "restores", "trash"} {
		if err := os.MkdirAll(filepath.Join(s.Root, dir), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SnapshotDir(id string) string { return filepath.Join(s.Root, "snapshots", id) }
func (s *Store) DataDir(id string) string     { return filepath.Join(s.SnapshotDir(id), "data") }
func (s *Store) RestoresDir() string          { return filepath.Join(s.Root, "restores") }
func (s *Store) TrashDir(id string) string    { return filepath.Join(s.Root, "trash", id) }

// New returns metadata for a snapshot that has not been taken yet.
func New(folder string, kind Kind) *Snapshot {
	now := time.Now()
	return &Snapshot{Version: formatVersion, ID: NewID(now), CreatedAt: now, Folder: folder, Kind: kind}
}

// Save writes the snapshot's meta.json.
func (s *Store) Save(snap *Snapshot) error {
	return WriteJSON(filepath.Join(s.SnapshotDir(snap.ID), "meta.json"), snap)
}

// Load reads one snapshot's meta.json.
func (s *Store) Load(id string) (*Snapshot, error) {
	var snap Snapshot
	if err := ReadJSON(filepath.Join(s.SnapshotDir(id), "meta.json"), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ReadJSON reads a JSON file written by WriteJSON into v.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s is broken: %w", path, err)
	}
	return nil
}

// List returns every snapshot, newest first.
func (s *Store) List() ([]*Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snaps []*Snapshot
	for _, e := range entries {
		snap, err := s.Load(e.Name())
		// A folder without meta.json is a snapshot that was interrupted
		// before it finished; it is not usable, so leave it out.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	// IDs start with the timestamp, so sorting by ID sorts by time.
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].ID > snaps[j].ID })
	return snaps, nil
}

// Find returns the one snapshot whose ID starts with prefix.
func (s *Store) Find(prefix string) (*Snapshot, error) {
	if len(prefix) < MinPrefix {
		return nil, fmt.Errorf("snapshot ID %q is too short: give at least %d characters", prefix, MinPrefix)
	}
	prefix = strings.ToUpper(prefix)
	snaps, err := s.List()
	if err != nil {
		return nil, err
	}
	var found *Snapshot
	for _, snap := range snaps {
		if strings.HasPrefix(snap.ID, prefix) {
			if found != nil {
				return nil, fmt.Errorf("snapshot ID %q matches more than one snapshot: type more characters", prefix)
			}
			found = snap
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no snapshot with ID %q", prefix)
	}
	return found, nil
}

// Delete removes a snapshot and its data.
func (s *Store) Delete(id string) error {
	return os.RemoveAll(s.SnapshotDir(id))
}

// crockford is Crockford's base32 alphabet: no I, L, O or U, so IDs are
// easy to read aloud and type.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a 26-character ULID-style ID: 10 characters of millisecond
// timestamp followed by 16 random characters. Because the timestamp comes
// first and has a fixed width, sorting IDs as strings sorts them by time.
func NewID(t time.Time) string {
	var id [26]byte
	ms := uint64(t.UnixMilli())
	for i := 9; i >= 0; i-- {
		id[i] = crockford[ms&31]
		ms >>= 5
	}
	var random [16]byte
	_, _ = rand.Read(random[:]) // crypto/rand.Read never fails on macOS
	for i, b := range random {
		id[10+i] = crockford[b&31]
	}
	return string(id[:])
}

// WriteJSON writes v to path atomically: a crash leaves either the old file
// or the new one, never half of one. It writes a temp file in the same
// folder, flushes it to disk, then renames it over the target, because a
// rename within one volume replaces the file in a single step.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

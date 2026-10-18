package store

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewIDSortsByTime(t *testing.T) {
	early := NewID(time.UnixMilli(1_000))
	late := NewID(time.UnixMilli(2_000))
	if len(early) != 26 || len(late) != 26 {
		t.Fatalf("IDs should be 26 characters: %q %q", early, late)
	}
	if early >= late {
		t.Errorf("earlier ID %q should sort before later ID %q", early, late)
	}
	for _, c := range early {
		if !strings.ContainsRune(crockford, c) {
			t.Errorf("ID %q has character %q outside the alphabet", early, c)
		}
	}
}

func TestSaveLoadList(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	first := New("/tmp/a", KindManual)
	second := New("/tmp/b", KindRun)
	second.ID = NewID(first.CreatedAt.Add(time.Second))
	code := 3
	second.ExitCode = &code
	for _, snap := range []*Snapshot{first, second} {
		if err := s.Save(snap); err != nil {
			t.Fatal(err)
		}
	}
	// An interrupted snapshot has a folder but no meta.json.
	if err := os.MkdirAll(s.DataDir("01INTERRUPTED"), 0o700); err != nil {
		t.Fatal(err)
	}

	snaps, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != second.ID || snaps[1].ID != first.ID {
		t.Fatalf("List should return the two saved snapshots newest first, got %+v", snaps)
	}
	if snaps[0].ExitCode == nil || *snaps[0].ExitCode != 3 || snaps[1].ExitCode != nil {
		t.Errorf("exit codes not kept: %v %v", snaps[0].ExitCode, snaps[1].ExitCode)
	}
}

func TestFind(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	for _, id := range []string{"01AAAAXXXX", "01AABBYYYY", "01CCCCZZZZ"} {
		if err := s.Save(&Snapshot{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		prefix, want, errHas string
	}{
		{prefix: "01cc", want: "01CCCCZZZZ"}, // case-insensitive
		{prefix: "01AAB", want: "01AABBYYYY"},
		{prefix: "01AA", errHas: "more than one"},
		{prefix: "01A", errHas: "too short"},
		{prefix: "01DD", errHas: "no snapshot"},
	}
	for _, tt := range tests {
		got, err := s.Find(tt.prefix)
		if tt.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("Find(%q): want error containing %q, got %v", tt.prefix, tt.errHas, err)
			}
			continue
		}
		if err != nil || got.ID != tt.want {
			t.Errorf("Find(%q) = %v, %v; want %s", tt.prefix, got, err, tt.want)
		}
	}
}

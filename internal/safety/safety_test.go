package safety

import "testing"

func TestCheck(t *testing.T) {
	const home = "/Users/ada"
	const store = "/Users/ada/.oops"
	tests := []struct {
		folder string
		ok     bool
	}{
		{"/Users/ada/code/app", true},
		{"/Users/ada/.oopsie", true}, // shares a prefix with the store, but is not inside it
		{"/", false},
		{"/System", false},
		{"/Users", false},
		{"/users", false}, // case-insensitive
		{"/Users/ada", false},
		{"/Users/ada/", false},
		{"/Users/ada/.oops", false},
		{"/Users/ada/.oops/snapshots/x", false},
	}
	for _, tt := range tests {
		err := Check(tt.folder, home, store)
		if (err == nil) != tt.ok {
			t.Errorf("Check(%q): ok=%v, got error %v", tt.folder, tt.ok, err)
		}
	}

	// A store outside home: a folder that contains it is refused.
	if err := Check("/Volumes/work", home, "/Volumes/work/.oops"); err == nil {
		t.Error("a folder containing the store should be refused")
	}
}

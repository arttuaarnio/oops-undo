package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests build the real binary and run it with HOME set to a temp
// folder, so they check exit codes the way a shell or script would see them.

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oops-test-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "oops")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// oops runs the binary in a fresh home folder and returns its exit code and output.
func oops(t *testing.T, home string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// project returns a home folder with one project folder in it.
func project(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestRunReturnsTheCommandsExitCode(t *testing.T) {
	home, dir := project(t)
	tests := []struct {
		args []string
		want int
	}{
		{[]string{"true"}, 0},
		{[]string{"sh", "-c", "exit 3"}, 3},
		{[]string{"no-such-command-oops"}, 127},
	}
	for _, tt := range tests {
		args := append([]string{"run", "--dir", dir, "--"}, tt.args...)
		if code, out := oops(t, home, args...); code != tt.want {
			t.Errorf("oops run %v: exit %d, want %d\n%s", tt.args, code, tt.want, out)
		}
	}
}

func TestRunStrictSkipsCommandWhenSnapshotFails(t *testing.T) {
	home, dir := project(t)
	marker := filepath.Join(dir, "ran")
	missing := filepath.Join(home, "missing")

	code, out := oops(t, home, "run", "--strict", "--dir", missing, "--", "touch", marker)
	if code != exitFailed {
		t.Errorf("exit %d, want %d\n%s", code, exitFailed, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--strict must not run the command when the snapshot fails")
	}

	// Without --strict the command still runs.
	if code, out := oops(t, home, "run", "--dir", missing, "--", "touch", marker); code != 0 {
		t.Errorf("exit %d, want 0\n%s", code, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("without --strict the command should run even if the snapshot fails")
	}
}

func TestUsageErrorsExitWithOne(t *testing.T) {
	home, _ := project(t)
	for _, args := range [][]string{{}, {"bogus"}, {"run"}, {"diff"}, {"restore"}} {
		if code, out := oops(t, home, args...); code != exitUsage {
			t.Errorf("oops %v: exit %d, want %d\n%s", args, code, exitUsage, out)
		}
	}
}

func TestRestoreRefusesWhileARestoreIsInterrupted(t *testing.T) {
	home, dir := project(t)
	if code, out := oops(t, home, "snap", "--dir", dir); code != 0 {
		t.Fatalf("snap: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A leftover journal is what a crash in the middle of a restore leaves behind.
	journal := filepath.Join(home, ".oops", "restores", "01INTERRUPTED", "journal.json")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte(`{"folder":"`+dir+`","steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, log := oops(t, home, "log", "--dir", dir, "--json")
	id := strings.Split(strings.Split(log, `"id": "`)[1], `"`)[0]
	code, out := oops(t, home, "restore", id, "--yes")
	if code != exitFailed || !strings.Contains(out, "--rollback") {
		t.Errorf("exit %d, want %d with a hint to roll back\n%s", code, exitFailed, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Error("a refused restore must not change the folder")
	}
}

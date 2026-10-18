// Command oops snapshots a folder before risky commands and restores it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arttuaarnio/oops-undo/internal/safety"
	"github.com/arttuaarnio/oops-undo/internal/snapshot"
	"github.com/arttuaarnio/oops-undo/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK     = 0
	exitUsage  = 1
	exitFailed = 2
)

const usage = `oops: undo for your shell

Usage:
  oops run [--dir PATH] [--strict] [--force] -- <command> [args...]
  oops snap [--dir PATH] [-m MESSAGE] [--force]
  oops log [--dir PATH] [--all] [-n N] [--json]
  oops diff <id> [PATH] [--exact]
  oops restore <id> [PATH] [--yes] [--dry-run] [--no-git] [--force]
  oops restore --rollback

Global flags (before the command):
  --verbose  --quiet  --no-color  --version
`

// app holds what every command needs: the store and output settings.
type app struct {
	store   *store.Store
	home    string
	out     io.Writer
	verbose bool
	quiet   bool
	color   bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	global := flag.NewFlagSet("oops", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	verbose := global.Bool("verbose", false, "")
	quiet := global.Bool("quiet", false, "")
	noColor := global.Bool("no-color", false, "")
	showVersion := global.Bool("version", false, "")
	if err := global.Parse(args); err != nil {
		return exitUsage
	}
	if *showVersion {
		fmt.Println("oops", version)
		return exitOK
	}
	if global.NArg() == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}

	st, err := store.Default()
	if err == nil {
		err = st.Init()
	}
	if err != nil {
		return fail(err, "Check that your home folder is writable.")
	}
	home, err := filepath.EvalSymlinks(filepath.Dir(st.Root))
	if err != nil {
		return fail(err, "Check that your home folder exists.")
	}
	a := &app{store: st, home: home, out: os.Stdout, verbose: *verbose, quiet: *quiet,
		color: !*noColor && os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout)}

	cmd, rest := global.Arg(0), global.Args()[1:]
	switch cmd {
	case "run":
		return a.cmdRun(rest)
	case "snap":
		return a.cmdSnap(rest)
	case "log":
		return a.cmdLog(rest)
	case "diff":
		return a.cmdDiff(rest)
	case "restore":
		return a.cmdRestore(rest)
	default:
		fmt.Fprintf(os.Stderr, "oops: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

// take checks that folder is safe, then snapshots it.
func (a *app) take(folder string, kind store.Kind, force bool, fill func(*store.Snapshot)) (*store.Snapshot, error) {
	if !force {
		if err := a.checkSafe(folder); err != nil {
			return nil, fmt.Errorf("refusing to snapshot: %w (use --force to override)", err)
		}
	}
	snap := store.New(folder, kind)
	if fill != nil {
		fill(snap)
	}
	if err := snapshot.Take(a.store, snap); err != nil {
		return nil, err
	}
	if snap.FileCount > 100_000 {
		a.warn(fmt.Sprintf("%s has %d files; snapshots of very large folders pause writes while they run.",
			a.tilde(folder), snap.FileCount))
	}
	if a.verbose {
		fmt.Fprintf(os.Stderr, "oops: snapshot %s: %d files in %d ms\n", short(snap.ID), snap.FileCount, snap.DurationMS)
	}
	return snap, nil
}

// checkSafe refuses system folders, home and anything overlapping the store.
func (a *app) checkSafe(folder string) error {
	storeRoot, err := filepath.EvalSymlinks(a.store.Root)
	if err != nil {
		return err
	}
	return safety.Check(folder, a.home, storeRoot)
}

// resolveDir turns --dir (or the current folder) into an absolute path with
// symlinks resolved, so the same folder always gets the same name.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// parseInterspersed lets flags come after positional arguments, as in
// "oops diff 01J9 --exact"; the flag package alone stops at the first one.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("oops "+name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs
}

// fail prints an error as one line saying what went wrong and one line
// saying what to do, and returns the "operation failed" exit code.
func fail(err error, hint string) int {
	fmt.Fprintf(os.Stderr, "oops: %v\n", err)
	if hint == "" {
		hint = hintFor(err)
	}
	if hint != "" {
		fmt.Fprintf(os.Stderr, "  %s\n", hint)
	}
	return exitFailed
}

func hintFor(err error) string {
	switch {
	case errors.Is(err, snapshot.ErrNotClonable):
		return "oops only works on folders on the same APFS volume as ~/.oops (usually your startup disk)."
	case errors.Is(err, os.ErrPermission):
		return "macOS may be blocking access: give your terminal app access in System Settings > Privacy & Security."
	}
	return ""
}

func (a *app) warn(msg string) {
	fmt.Fprintf(os.Stderr, "oops: warning: %s\n", msg)
}

// say prints normal output unless --quiet is set.
func (a *app) say(format string, args ...any) {
	if !a.quiet {
		fmt.Fprintf(a.out, format+"\n", args...)
	}
}

// tilde shortens paths in the home folder to ~/...
func (a *app) tilde(path string) string {
	if path == a.home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, a.home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// short is the ID shown to people: the 10 timestamp characters.
func short(id string) string { return id[:10] }

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

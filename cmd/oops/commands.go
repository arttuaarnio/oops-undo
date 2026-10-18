package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/arttuaarnio/oops-undo/internal/restore"
	"github.com/arttuaarnio/oops-undo/internal/store"
)

func (a *app) cmdRun(args []string) int {
	fs := newFlagSet("run")
	dir := fs.String("dir", "", "")
	strict := fs.Bool("strict", false, "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	command := fs.Args()
	if len(command) == 0 {
		fs.Usage()
		return exitUsage
	}

	folder, err := resolveDir(*dir)
	var snap *store.Snapshot
	if err == nil {
		snap, err = a.take(folder, store.KindRun, *force, func(s *store.Snapshot) {
			s.Command = strings.Join(command, " ")
		})
	}
	if err != nil {
		if *strict {
			return fail(fmt.Errorf("no snapshot taken, so the command was not run: %w", err), "")
		}
		// oops adds safety, not friction: a failed snapshot never stops the command.
		a.warn(fmt.Sprintf("no snapshot taken: %v", err))
	}

	code := runCommand(command)
	if snap != nil {
		snap.ExitCode = &code
		if err := a.store.Save(snap); err != nil {
			a.warn(fmt.Sprintf("could not record the exit code: %v", err))
		}
	}
	return code
}

// runCommand runs the command with oops's stdin, stdout, stderr and
// environment, and returns its exit code the way a shell would.
func runCommand(args []string) int {
	// Ctrl-C reaches the whole foreground process group, so the command gets
	// it as usual. oops catches it only so it survives to record the exit
	// code. (signal.Ignore would not work: ignored signals are inherited by
	// the child, which would then ignore Ctrl-C too.)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(signals)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()) // shell convention for "killed by a signal"
		}
		return exitErr.ExitCode()
	default:
		fmt.Fprintf(os.Stderr, "oops: %v\n", err)
		return 127 // shell convention for "command not found"
	}
}

func (a *app) cmdSnap(args []string) int {
	fs := newFlagSet("snap")
	dir := fs.String("dir", "", "")
	message := fs.String("m", "", "")
	force := fs.Bool("force", false, "")
	if _, err := parseInterspersed(fs, args); err != nil {
		return exitUsage
	}
	folder, err := resolveDir(*dir)
	if err != nil {
		return fail(err, "Check that the folder exists.")
	}
	snap, err := a.take(folder, store.KindManual, *force, func(s *store.Snapshot) { s.Message = *message })
	if err != nil {
		return fail(err, "")
	}
	a.say("Saved %s as snapshot %s (%d files, %d ms).", a.tilde(folder), short(snap.ID), snap.FileCount, snap.DurationMS)
	return exitOK
}

func (a *app) cmdLog(args []string) int {
	fs := newFlagSet("log")
	dir := fs.String("dir", "", "")
	all := fs.Bool("all", false, "")
	n := fs.Int("n", 20, "")
	asJSON := fs.Bool("json", false, "")
	if _, err := parseInterspersed(fs, args); err != nil {
		return exitUsage
	}
	folder, err := resolveDir(*dir)
	if err != nil && !*all {
		return fail(err, "Check that the folder exists, or use --all.")
	}
	snaps, err := a.store.List()
	if err != nil {
		return fail(err, "")
	}

	shown := []*store.Snapshot{} // not nil, so --json prints [] rather than null
	for _, s := range snaps {
		if len(shown) == *n {
			break
		}
		if *all || s.Folder == folder {
			shown = append(shown, s)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(shown); err != nil {
			return fail(err, "")
		}
		return exitOK
	}
	if len(shown) == 0 {
		a.say("No snapshots yet for %s. Take one with: oops snap", a.tilde(folder))
		return exitOK
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN\tFOLDER\tKIND\tEXIT\tCOMMAND")
	for _, s := range shown {
		exit := "-"
		if s.ExitCode != nil {
			exit = fmt.Sprint(*s.ExitCode)
		}
		what := s.Command
		if what == "" {
			what = s.Message
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", short(s.ID), ago(s.CreatedAt), a.tilde(s.Folder), s.Kind, exit, what)
	}
	if err := w.Flush(); err != nil {
		return fail(err, "")
	}
	return exitOK
}

func (a *app) cmdDiff(args []string) int {
	fs := newFlagSet("diff")
	exact := fs.Bool("exact", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) == 0 || len(pos) > 2 {
		fs.Usage()
		return exitUsage
	}
	snap, err := a.store.Find(pos[0])
	if err != nil {
		return fail(err, "List snapshots with: oops log --all")
	}
	path, err := subPath(pos)
	if err != nil {
		return fail(err, "Give a path relative to "+a.tilde(snap.Folder)+".")
	}
	changes, err := restore.Diff(a.store.DataDir(snap.ID), snap.Folder, path, *exact)
	if err != nil {
		return fail(err, "")
	}
	if len(changes) == 0 {
		a.say("No changes since snapshot %s.", short(snap.ID))
	}
	a.printChanges(changes)
	return exitOK
}

func (a *app) cmdRestore(args []string) int {
	fs := newFlagSet("restore")
	yes := fs.Bool("yes", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	noGit := fs.Bool("no-git", false, "")
	force := fs.Bool("force", false, "")
	rollback := fs.Bool("rollback", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	if *rollback {
		folders, err := restore.RollbackPending(a.store)
		for _, f := range folders {
			a.say("Undid the interrupted restore of %s.", a.tilde(f))
		}
		if err != nil {
			return fail(err, "Check the journal in ~/.oops/restores/; the pre-restore snapshot also holds your files.")
		}
		if len(folders) == 0 {
			a.say("No interrupted restores.")
		}
		return exitOK
	}
	if len(pos) == 0 || len(pos) > 2 {
		fs.Usage()
		return exitUsage
	}

	snap, err := a.store.Find(pos[0])
	if err != nil {
		return fail(err, "List snapshots with: oops log --all")
	}
	path, err := subPath(pos)
	if err != nil {
		return fail(err, "Give a path relative to "+a.tilde(snap.Folder)+".")
	}
	target := a.tilde(filepath.Join(snap.Folder, path))
	if !*force {
		if err := a.checkSafe(snap.Folder); err != nil {
			return fail(fmt.Errorf("refusing to restore: %w", err), "Use --force to override.")
		}
	}

	changes, err := restore.Diff(a.store.DataDir(snap.ID), snap.Folder, path, false)
	if err != nil {
		return fail(err, "")
	}
	if *noGit {
		changes = withoutGit(changes)
	}
	if len(changes) == 0 {
		a.say("Nothing to restore: %s already matches snapshot %s.", target, short(snap.ID))
		return exitOK
	}
	a.say("Restore %s to snapshot %s from %s: %s", target, short(snap.ID), ago(snap.CreatedAt), summary(changes))
	if *dryRun {
		a.printChanges(changes)
		a.say("Dry run: nothing was changed.")
		return exitOK
	}
	if _, err := os.Stat(filepath.Join(snap.Folder, ".git", "index.lock")); err == nil && !*noGit {
		a.warn(".git/index.lock exists, so a git command may be running. Restoring .git now can corrupt it; consider --no-git.")
	}
	if !*yes && !confirm("Continue?") {
		a.say("Nothing was changed.")
		return exitOK
	}

	pre, err := restore.Run(a.store, snap, restore.Options{Path: path, NoGit: *noGit})
	if err != nil {
		hint := ""
		if errors.Is(err, restore.ErrPending) {
			hint = "Undo it with: oops restore --rollback, then try again."
		} else if pre != nil {
			hint = "Your files from before this attempt are also saved as snapshot " + short(pre.ID) + "."
		}
		return fail(err, hint)
	}
	a.say("Restored %s to its state %s (previous state saved as %s).", target, ago(snap.CreatedAt), short(pre.ID))
	return exitOK
}

// subPath returns the optional PATH argument, cleaned, or "" for the whole folder.
func subPath(pos []string) (string, error) {
	if len(pos) < 2 {
		return "", nil
	}
	p := filepath.Clean(pos[1])
	if p == "." {
		return "", nil
	}
	if !filepath.IsLocal(p) {
		return "", fmt.Errorf("path %q must be relative and stay inside the snapshotted folder", pos[1])
	}
	return p, nil
}

func withoutGit(changes []restore.Change) []restore.Change {
	var kept []restore.Change
	for _, c := range changes {
		if c.Path != ".git/" && !strings.HasPrefix(c.Path, ".git/") {
			kept = append(kept, c)
		}
	}
	return kept
}

// summary describes what a restore will do, from the diff's point of view
// reversed: deleted paths come back, added paths go away.
func summary(changes []restore.Change) string {
	counts := map[byte]int{}
	for _, c := range changes {
		counts[c.Op]++
	}
	return fmt.Sprintf("%d to bring back, %d to revert, %d to remove.", counts['D'], counts['M'], counts['A'])
}

func (a *app) printChanges(changes []restore.Change) {
	colors := map[byte]string{'A': "\033[32m", 'M': "\033[33m", 'D': "\033[31m"}
	for _, c := range changes {
		if a.color {
			fmt.Fprintf(a.out, "  %s%c\033[0m  %s\n", colors[c.Op], c.Op, c.Path)
		} else {
			fmt.Fprintf(a.out, "  %c  %s\n", c.Op, c.Path)
		}
	}
}

func confirm(question string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// ago formats a time as "12 seconds ago", "3 hours ago" and so on.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return plural(int(d.Seconds()), "second") + " ago"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	default:
		return plural(int(d.Hours()/24), "day") + " ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

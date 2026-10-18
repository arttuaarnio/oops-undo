# How oops works

What oops does under the hood, and why it is built this way.

## Copy-on-write clones (`clonefile`)

A normal copy reads every byte and writes it again. On APFS, `clonefile(2)` makes a new file or folder that *points at the same disk blocks* as the original. Nothing is copied. Only when one side changes does APFS write the changed blocks somewhere new, so the two sides drift apart. That is why a snapshot of 10,000 files takes about 0.15 s and almost no disk space.

Three limits shape the code (`internal/snapshot/snapshot.go`):

- **Same volume only.** A clone shares blocks, and blocks belong to one volume. Cloning to another disk fails with `EXDEV` ("cross-device"). `CheckVolume` compares the filesystem IDs from `statfs` to catch this early with a clear message.
- **One top-level entry at a time.** `clonefile` *can* clone a whole folder in one call, but while it runs the kernel blocks every write to that folder tree, and Apple says not to do it for big trees. Cloning `src/`, then `build/`, then `README.md` keeps each pause short.
- **`CLONE_NOFOLLOW`** clones a symlink itself instead of the file it points to. Without it, a link to `/etc/hosts` would be copied as the contents of `/etc/hosts`.

## Atomic rename

`rename(2)` within one volume does not move data. It changes one directory entry in a single step, so anyone looking sees either the old name or the new one, never something in between. Two places rely on this:

- `store.WriteJSON` writes a temp file, calls `Sync` (flush to disk), then renames it over `meta.json`. A crash leaves the old file or the new one, never half a file.
- Restore uses `renamex_np` with two macOS-only flags:
  - `RENAME_SWAP` exchanges two entries in one step. The live `src/` and the snapshot's copy of `src/` trade places, so `src/` is never missing.
  - `RENAME_EXCL` renames only if the target doesn't exist, so restore can't overwrite something by accident.

The project folder itself is never renamed. A shell (or Claude Code) sitting inside it holds a reference to that folder; replacing the folder would leave the shell inside a deleted one.

## The restore journal and inode numbers

Every file and folder has an **inode number**, its ID inside the volume. A clone gets a new inode. A rename keeps the inode but moves it.

Before restoring, oops writes a journal listing each step and the inode that sits at each path *now*. Every step changes which inode is at that path. So, later, comparing "inode at this path now" with "inode in the journal" tells whether the step ran:

| Now vs journal | Meaning | Undo |
| --- | --- | --- |
| same | step never ran | nothing |
| journal said 0 (no entry) | entry was added | move it back out |
| now 0 | entry was removed | move it back in |
| different | entry was swapped | swap back |

Because of this the journal never needs updating mid-restore, the same rollback code handles a failed step and a crash, and running rollback twice is harmless (`internal/restore/restore.go`, `undo`).

## IDs that sort by time

`store.NewID` builds a 26-character ID: 10 characters of millisecond timestamp, then 16 random ones, in Crockford's base32 (no I, L, O, U, so no confusing 1/I or 0/O). Fixed width plus timestamp first means sorting the IDs as text sorts them by time, so `List` needs no separate date sort and `log` shows the first 10 characters.

## Exit codes and signals in `oops run`

`oops run` exits with the wrapped command's code, like a shell: the command's own code, `128 + signal` if it was killed (Ctrl-C is signal 2, so 130), or `127` if it wasn't found. Ctrl-C goes to every process in the terminal's foreground group, so the command gets it directly. oops *catches* it (`signal.Notify`) rather than *ignoring* it, because an ignored signal is inherited by the child, and the command would then ignore Ctrl-C too.

## Testing filesystem code

Tests use real folders from `t.TempDir()` and compare whole trees (type, mode, mtime, content, link target) before and after. The most important test makes a restore fail on purpose: it sets the `uchg` (immutable) flag on `src/`, so the final swap gets "operation not permitted" after seven entries were already swapped, and then checks the folder is exactly as it was.

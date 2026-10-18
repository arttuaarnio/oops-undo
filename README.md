# oops — undo for your shell

[![ci](https://github.com/arttuaarnio/oops-undo/actions/workflows/ci.yml/badge.svg)](https://github.com/arttuaarnio/oops-undo/actions/workflows/ci.yml)

Snapshot a folder before a risky command, then restore the whole folder or a single path. macOS only, using APFS copy-on-write clones: snapshots are near-instant and take almost no disk space.

Early version (v0.1). The Claude Code hook coming next.

## Why

AI agents and tired humans run `rm -rf`, `git clean` and `git reset --hard` on the wrong thing. Claude Code's rewind doesn't undo Bash commands, and git-based undo misses untracked and ignored files. `oops` snapshots everything in the folder.

## Install

Requires macOS on APFS and Go 1.26+.

```sh
go install github.com/arttuaarnio/oops-undo/cmd/oops@latest
```

## Usage

```sh
$ oops run -- rm -r build src/legacy
$ oops log
ID          WHEN            FOLDER        KIND  EXIT  COMMAND
01M57C72CA  12 seconds ago  ~/code/myapp  run   0     rm -r build src/legacy
$ oops restore 01M57C72CA src/legacy
Restore ~/code/myapp/src/legacy to snapshot 01M57C72CA from 30 seconds ago: 1 to bring back, 0 to revert, 0 to remove.
Continue? [y/N] y
Restored ~/code/myapp/src/legacy to its state 30 seconds ago (previous state saved as 01M57C72E8).
```

| Command | |
| --- | --- |
| `oops run [--dir PATH] [--strict] -- <cmd>` | Snapshot, then run the command. `--strict` skips the command if the snapshot fails. |
| `oops snap [--dir PATH] [-m MSG]` | Snapshot only. |
| `oops log [--dir PATH] [--all] [-n N] [--json]` | List snapshots, newest first. |
| `oops diff <id> [PATH] [--exact]` | Show added, modified and deleted paths since the snapshot. |
| `oops restore <id> [PATH] [--dry-run] [--yes] [--no-git]` | Restore the folder or one path. The current state is saved first, so restores can be undone. |
| `oops restore --rollback` | Undo a restore interrupted by a crash. |

IDs accept any unique prefix of 4+ characters. System folders, your home folder and `~/.oops` are refused unless you pass `--force`.

## How it works

Snapshots are `clonefile(2)` copies in `~/.oops`, about 0.15 s for 10,000 files. Restore swaps each entry back with an atomic rename, guided by a journal, so a failed restore rolls back cleanly. Details: [docs/how-it-works.md](docs/how-it-works.md).

## Limitations

- Covers only the snapshotted folder. `rm -rf ~` also deletes `~/.oops`.
- Covers only files: not databases, `git push` or anything remote.
- The folder must be on the same APFS volume as your home folder.
- Clones drop hard links, ACLs and setuid bits.
- No automatic cleanup yet; old snapshots stay in `~/.oops/snapshots/`.

## Similar tools

[bashward](https://docs.rs/bashward) is a Claude Code hook that clones the paths it parses from each command. `oops` snapshots the whole folder instead, so scripts and `make clean` are covered too, and it works without an agent. Claude Code checkpoints, `git stash` and Time Machine each cover part of the same ground.

## Roadmap

v0.2: Claude Code hook. v0.3: prune, config, `oops doctor`. v1.0: Homebrew.

## License

MIT

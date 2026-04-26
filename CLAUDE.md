# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this tool does

A Go CLI that suggests a git commit message for the changes in the user's
current directory and below. It reads the diff plus the most recent Claude
Code conversation transcript for the project, sends both to the local
`claude` CLI in non-interactive mode, and prompts the user to accept, edit,
or cancel before running `git commit`.

## Commands

Use the `Makefile`; `make help` lists targets.

```sh
make build      # builds ./gcam
make check      # fmt-check + vet + test (no external tools needed)
make lint       # golangci-lint (install separately)
make install    # install gcam to $(go env GOPATH)/bin (override with PREFIX=...)
```

## Architecture

The flow is linear and lives in `main.go::run`:

1. `internal/git` resolves the repo root, produces a cwd-scoped diff
   (`git diff HEAD -- .` for tracked files, plus `git diff --no-index`
   against `/dev/null` for each untracked file), and exposes
   `ChangedFiles` — `[]{Path, Mtime}` from `git status --porcelain`.
2. `internal/history` reads `~/.claude/projects/<encoded-root>/*.jsonl`
   across all sessions, parses each entry's ISO 8601 `timestamp`, and
   returns user/assistant text messages sorted by time, optionally filtered
   to those at-or-after a `since` cutoff. The encoded root is the absolute
   project path with `/` replaced by `-` (Claude Code's own convention).
3. `main.correlate` buckets transcript messages by changed file: each file
   gets the messages whose timestamp is within ±window of its mtime. The
   default window is 5 minutes (`--window`).
4. `main.buildPrompt` emits the diff plus a per-file "Likely intent"
   section showing the correlated messages. If no file matched any
   message it falls back to the last N messages globally — better than
   no context at all.
5. `internal/claudecli` shells out to `claude -p` with the prompt on stdin.
6. `internal/ui.Confirm` prints the suggestion and reads `a` / `e` / `c`.
   On edit it writes to a temp file, runs `$GIT_EDITOR`/`$EDITOR`/`vi`, and
   reads back the result. On accept (or successful edit) `main.runCommit`
   calls `git commit -m`.

The Claude Code CLI is the only model backend right now. The original
sketch had a `Provider` interface fronting Anthropic API / Bedrock too;
that was dropped in favour of the simpler single-path design. If multiple
backends are needed later, reintroduce the abstraction at the
`claudecli.Suggest` boundary.

## Things worth knowing

- The diff is scoped to cwd, not to the repo root. Running the tool in a
  subdir intentionally narrows the commit to that subtree.
- `git diff --no-index` exits 1 when files differ — `internal/git.run`
  treats exit 1 with non-empty stdout as success.
- Transcript reading is best-effort: missing or unparseable session files
  silently produce an empty transcript rather than failing the run.
- Each JSONL entry also carries `cwd`, `gitBranch`, and `sessionId` —
  currently unused but available for future filtering (e.g. excluding
  messages from a different working directory than where the changes are).

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
   gets the messages timestamped in `(lastCommitTime(file), file.mtime]` —
   i.e. the work-session window from the file's previous commit up to its
   current mtime. Untracked / never-committed files fall back to repo
   HEAD's commit time as the lower bound (zero if there's no HEAD yet).
   `main.compactBucket` then drops oldest messages from each bucket until
   it fits in `--max-bucket-chars` (default 8000).
4. `main.buildPrompt` emits the diff plus a per-file "Likely intent"
   section showing the correlated messages. If no file matched any
   message it falls back to the last N messages globally — better than
   no context at all.
5. Backend dispatch: by default, `internal/bedrock` POSTs the prompt to the
   Bedrock InvokeModel endpoint with bearer auth (`AWS_BEARER_TOKEN_BEDROCK`).
   If that env var is absent, gcam falls back to `internal/claudecli` which
   shells out to `claude -p`. Region picks up `AWS_REGION`/`AWS_DEFAULT_REGION`
   (default `us-east-1`); model id picks up `GCAM_BEDROCK_MODEL` or `--model`.
6. `internal/ui.Confirm` prints the suggestion in a styled box and shows a
   `huh` select (Accept / Edit / Cancel). On edit it writes to a temp file,
   runs `$GIT_EDITOR`/`$EDITOR`/`vi`, and reads back the result. On accept
   (or successful edit) `commit` calls `git add` + `git commit -m`.

`internal/config` resolves the commit-message style (traditional vs
Conventional Commits 1.0.0) before any of the above runs, and `buildPrompt`
emits a different system instruction depending on the result.

Two backends are wired up: the default Bedrock direct-API path (active
whenever `AWS_BEARER_TOKEN_BEDROCK` is set) and a `claude -p` fallback.
Dispatch lives in `main.suggestMessage` — both packages expose a
`Suggest(ctx, prompt, model) (string, error)` shape, so if a third backend
is added, lift the selector into a small interface rather than growing the
`if` ladder.

## Commit-message style configuration

Two styles are supported:

- `traditional` — concise imperative subject + optional body explaining WHY.
- `conventional` — Conventional Commits 1.0.0 (`<type>(scope)!: description`,
  body, `BREAKING CHANGE:` footers).

Resolution precedence (highest first), implemented in
`internal/config/config.go::Resolve`:

1. `--style traditional|conventional` CLI flag (one-off; not persisted).
2. `<repo-root>/.gcam.json` — checked into the repo so all collaborators
   share the same style. Schema: `{"style": "traditional"}` or
   `{"style": "conventional"}`. Invalid values fail loudly rather than
   silently falling back.
3. `$XDG_CONFIG_HOME/gcam/config.json`, falling back to
   `~/.config/gcam/config.json`. (We do not use `os.UserConfigDir` so the
   path is the same on macOS and Linux.)
4. Interactive first-run prompt (`ui.ChooseStyle`) — fires only when no
   config exists and stdin/stderr are both TTYs and `--print` was not
   supplied. The choice is saved to the user-level config; the per-project
   file is never written automatically.
5. Silent default of `conventional` for non-TTY runs (CI, pipes,
   `--print`) so the tool never crashes on huh in a non-interactive
   environment.

Source of the resolved style is logged once to stderr (e.g.
`gcam: style=conventional (project)`) unless `--print` is set.

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

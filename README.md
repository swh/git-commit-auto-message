# git-commit-auto-message

A small Go CLI that suggests a git commit message based on the diff in the
current directory (and below) plus the most recent Claude Code conversation
for the project. Run it from anywhere inside a git repo; it shells out to the
local `claude` CLI to produce the message.

## Install

```sh
go build -o gcam .
# put it on PATH, e.g.
mv gcam ~/.local/bin/
```

Requires the [Claude Code CLI](https://claude.com/claude-code) (`claude`)
installed and authenticated.

N.B. if you have the ohmyzsh git plugin installed you will need an `unalias gcam` line in your `.zshrc` file.

## Usage

```sh
# from anywhere inside a git repo:
gcam              # suggest, prompt to accept/edit/cancel, then commit
gcam --print      # just print the suggestion to stdout
gcam --no-history # ignore Claude Code transcript context
gcam --model claude-sonnet-4-6
gcam --style conventional   # one-off override (traditional|conventional)
```

The diff is scoped to the current directory and below (so running it in a
subdir limits the commit to that subtree's changes).

## Commit-message style

`gcam` can produce either traditional commit messages or
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).

On first run `gcam` asks which you prefer and saves the answer to
`~/.config/gcam/config.json` (or `$XDG_CONFIG_HOME/gcam/config.json`).
Override per-repo by checking in a `.gcam.json` at the repo root so all
collaborators get the same style:

```json
{ "style": "conventional" }
```

Precedence: `--style` flag > `.gcam.json` (project) > user config >
first-run prompt > silent default (`conventional`) for non-TTY runs.

## How it builds the prompt

1. `git diff HEAD -- .` (staged + unstaged vs HEAD), plus synthetic diffs for
   untracked files.
2. The most recent Claude Code session transcript for the project, read from
   `~/.claude/projects/<encoded-root>/*.jsonl`.
3. Both are concatenated into a single prompt and piped to `claude -p`.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/pflag"

	"github.com/swh/git-commit-auto-message/internal/claudecli"
	"github.com/swh/git-commit-auto-message/internal/config"
	"github.com/swh/git-commit-auto-message/internal/git"
	"github.com/swh/git-commit-auto-message/internal/history"
	"github.com/swh/git-commit-auto-message/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type mode int

const (
	modeAll        mode = iota // stage everything (current behaviour)
	modeStagedOnly             // commit only the pre-staged set
)

func run() error {
	var (
		model         = pflag.StringP("model", "m", "", "override model id passed to `claude --model`")
		printOnly     = pflag.BoolP("print", "p", false, "print the message and exit; do not prompt or commit")
		noHistory     = pflag.BoolP("no-history", "n", false, "skip Claude Code conversation history")
		fallbackN     = pflag.Int("fallback-messages", 20, "max recent messages to include when no per-file correlation matches")
		maxMsgChar    = pflag.Int("max-message-chars", 800, "truncate each transcript message to this many chars")
		maxBucketChar = pflag.Int("max-bucket-chars", 8000, "max chars of transcript per file; oldest messages dropped to fit")
		style         = pflag.String("style", "", "commit message style: traditional|conventional (overrides config)")
		hints         = pflag.StringArrayP("hint", "H", nil, "extra context for the model (repeatable, e.g. -H 'fixes #123' -H 'preparing for v2 release')")
	)
	pflag.Parse()

	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := git.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("not in a git repo: %w", err)
	}

	interactive := !*printOnly &&
		isatty.IsTerminal(os.Stdin.Fd()) &&
		isatty.IsTerminal(os.Stderr.Fd())
	resolved, err := config.Resolve(config.Style(*style), root, interactive, ui.ChooseStyle)
	if err != nil {
		return err
	}
	if !*printOnly {
		fmt.Fprintf(os.Stderr, "gcam: style=%s (%s)\n", resolved.Style, resolved.Source)
	}

	allFiles, err := git.ChangedFiles(cwd)
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(allFiles) == 0 {
		return errors.New("no changes detected in current directory or below")
	}

	var staged, other []git.ChangedFile
	for _, f := range allFiles {
		if f.IsStaged() {
			staged = append(staged, f)
		}
		if f.HasUnstaged() {
			other = append(other, f)
		}
	}

	files, m, err := chooseMode(cwd, allFiles, staged, other, *printOnly)
	if err != nil {
		return err
	}
	if files == nil {
		fmt.Fprintln(os.Stderr, "cancelled")
		return nil
	}

	if !*printOnly {
		announceMode(cwd, files, m)
	}

	var diff string
	if m == modeStagedOnly {
		diff, err = git.DiffStaged(cwd)
	} else {
		diff, err = git.DiffScoped(cwd)
	}
	if err != nil {
		return fmt.Errorf("git diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return errors.New("selected set has no diff content")
	}

	var buckets []fileBucket
	var fallback []history.Message
	if !*noHistory {
		lowers := lowerBounds(root, files)
		since := earliestNonZero(lowers)
		msgs, _ := history.Recent(root, since)
		buckets = correlate(files, lowers, msgs)
		for i := range buckets {
			buckets[i].Messages = compactBucket(buckets[i].Messages, *maxMsgChar, *maxBucketChar)
		}
		if !anyBucketHasMessages(buckets) {
			fallback = lastN(msgs, *fallbackN)
		}
	}

	prompt := buildPrompt(cwd, diff, buckets, fallback, *maxMsgChar, resolved.Style, *hints)

	msg, err := claudecli.Suggest(ctx, prompt, *model)
	if err != nil {
		return err
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return errors.New("claude returned an empty message")
	}

	if *printOnly {
		fmt.Println(msg)
		return nil
	}

	for {
		final, action, err := ui.Confirm(msg)
		if err != nil {
			return err
		}
		switch action {
		case ui.Cancel:
			fmt.Fprintln(os.Stderr, "cancelled")
			return nil
		case ui.Edit:
			msg = final
			continue
		case ui.Accept:
			return commit(cwd, files, m, final)
		}
	}
}

// chooseMode decides which set of files to commit. If both pre-staged and
// other changes exist, the user is prompted (unless --print, in which case
// we default to "all" so the suggestion is broadest). Returns nil files when
// the user cancels.
func chooseMode(cwd string, all, staged, other []git.ChangedFile, printOnly bool) ([]git.ChangedFile, mode, error) {
	hasStaged, hasOther := len(staged) > 0, len(other) > 0
	if hasStaged && hasOther {
		if printOnly {
			return all, modeAll, nil
		}
		choice, err := ui.ChooseStageMode(renderShort(cwd, staged), renderShort(cwd, other))
		if err != nil {
			return nil, 0, err
		}
		switch choice {
		case ui.ChoiceStagedOnly:
			return staged, modeStagedOnly, nil
		case ui.ChoiceStageAll:
			return all, modeAll, nil
		default:
			return nil, 0, nil
		}
	}
	if hasStaged {
		return staged, modeStagedOnly, nil
	}
	return all, modeAll, nil
}

func announceMode(cwd string, files []git.ChangedFile, m mode) {
	switch m {
	case modeStagedOnly:
		fmt.Fprintf(os.Stderr, "Will commit %d already-staged file(s):\n", len(files))
	case modeAll:
		fmt.Fprintf(os.Stderr, "Will stage and commit %d file(s):\n", len(files))
	}
	fmt.Fprint(os.Stderr, renderShort(cwd, files))
	fmt.Fprintln(os.Stderr)
}

func renderShort(cwd string, files []git.ChangedFile) string {
	var b strings.Builder
	for _, f := range files {
		rel, err := filepath.Rel(cwd, f.Path)
		if err != nil {
			rel = f.Path
		}
		fmt.Fprintf(&b, "  %s %s\n", f.Status, rel)
	}
	return b.String()
}

func commit(cwd string, files []git.ChangedFile, m mode, msg string) error {
	if m == modeAll {
		args := []string{"add", "--"}
		for _, f := range files {
			args = append(args, f.Path)
		}
		add := exec.Command("git", args...)
		add.Dir = cwd
		add.Stdout = os.Stdout
		add.Stderr = os.Stderr
		if err := add.Run(); err != nil {
			return fmt.Errorf("git add: %w", err)
		}
	}

	c := exec.Command("git", "commit", "-m", msg)
	c.Dir = cwd
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// fileBucket pairs a changed file with the transcript messages from the work
// session that produced its current state — bounded below by the file's last
// commit (or repo HEAD for never-committed files) and above by the file's
// mtime. Lower may be zero, meaning "no lower bound".
type fileBucket struct {
	File     git.ChangedFile
	Lower    time.Time
	Messages []history.Message
}

// lowerBounds resolves each file's "previous commit" timestamp. Untracked /
// never-committed files fall back to repo HEAD's commit time (zero if the
// repo has no commits yet).
func lowerBounds(repoRoot string, files []git.ChangedFile) []time.Time {
	out := make([]time.Time, len(files))
	headTime, _, _ := git.HeadTime(repoRoot)
	for i, f := range files {
		if t, ok, _ := git.LastCommitTime(repoRoot, f.Path); ok {
			out[i] = t
			continue
		}
		out[i] = headTime
	}
	return out
}

func correlate(files []git.ChangedFile, lowers []time.Time, msgs []history.Message) []fileBucket {
	buckets := make([]fileBucket, 0, len(files))
	for i, f := range files {
		if f.Mtime.IsZero() {
			buckets = append(buckets, fileBucket{File: f})
			continue
		}
		var related []history.Message
		for _, m := range msgs {
			if m.Time.After(f.Mtime) {
				continue
			}
			if !lowers[i].IsZero() && m.Time.Before(lowers[i]) {
				continue
			}
			related = append(related, m)
		}
		sort.Slice(related, func(i, j int) bool { return related[i].Time.Before(related[j].Time) })
		buckets = append(buckets, fileBucket{File: f, Lower: lowers[i], Messages: related})
	}
	return buckets
}

func earliestNonZero(ts []time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.IsZero() {
			continue
		}
		if out.IsZero() || t.Before(out) {
			out = t
		}
	}
	return out
}

// compactBucket trims a bucket's messages so that the total formatted text
// stays under maxBucketChars. Each message is first truncated by
// maxMsgChars; then oldest messages are dropped until the total fits.
// Newest messages (closest to the file's mtime) are preserved.
func compactBucket(msgs []history.Message, maxMsgChars, maxBucketChars int) []history.Message {
	if maxBucketChars <= 0 || len(msgs) == 0 {
		return msgs
	}
	total := 0
	for _, m := range msgs {
		total += formattedLen(m, maxMsgChars)
	}
	if total <= maxBucketChars {
		return msgs
	}
	for total > maxBucketChars && len(msgs) > 0 {
		total -= formattedLen(msgs[0], maxMsgChars)
		msgs = msgs[1:]
	}
	return msgs
}

func formattedLen(m history.Message, maxMsgChars int) int {
	text := strings.TrimSpace(m.Text)
	if maxMsgChars > 0 && len(text) > maxMsgChars {
		return maxMsgChars + 1 + len("[15:04:05] role: …\n")
	}
	return len(text) + len("[15:04:05] role: \n")
}

func anyBucketHasMessages(buckets []fileBucket) bool {
	for _, b := range buckets {
		if len(b.Messages) > 0 {
			return true
		}
	}
	return false
}

func lastN(msgs []history.Message, n int) []history.Message {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

func buildPrompt(cwd, diff string, buckets []fileBucket, fallback []history.Message, maxChars int, style config.Style, hints []string) string {
	var b strings.Builder
	b.WriteString(systemInstruction(style))
	b.WriteString("\n\n")

	if h := cleanHints(hints); len(h) > 0 {
		b.WriteString("# Hints from the user (treat as authoritative context for *why* this change was made)\n")
		for _, line := range h {
			fmt.Fprintf(&b, "- %s\n", line)
		}
		b.WriteString("\n")
	}

	b.WriteString("# Diff (scoped to the user's current directory and below)\n")
	b.WriteString("```diff\n")
	b.WriteString(diff)
	b.WriteString("\n```\n")

	if len(buckets) > 0 && anyBucketHasMessages(buckets) {
		b.WriteString("\n# Likely intent — transcript messages from the work session that produced each file's current state\n")
		b.WriteString("Window per file: from the file's previous commit (or repo HEAD for never-committed files) up to its current mtime. Use these to infer *why* the change was made, not *what*.\n")
		for _, bk := range buckets {
			if len(bk.Messages) == 0 {
				continue
			}
			rel, err := filepath.Rel(cwd, bk.File.Path)
			if err != nil {
				rel = bk.File.Path
			}
			fmt.Fprintf(&b, "\n## %s (modified %s", rel, bk.File.Mtime.Local().Format("2006-01-02 15:04:05"))
			if !bk.Lower.IsZero() {
				fmt.Fprintf(&b, ", since %s", bk.Lower.Local().Format("2006-01-02 15:04:05"))
			}
			b.WriteString(")\n")
			for _, m := range bk.Messages {
				b.WriteString(formatMessage(m, maxChars))
			}
		}
	} else if len(fallback) > 0 {
		b.WriteString("\n# Recent transcript (no per-file correlation matched, showing last messages for general context)\n")
		for _, m := range fallback {
			b.WriteString(formatMessage(m, maxChars))
		}
	}

	return b.String()
}

func cleanHints(hints []string) []string {
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		if s := strings.TrimSpace(h); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func systemInstruction(style config.Style) string {
	switch style {
	case config.StyleConventional:
		return "You write Conventional Commits 1.0.0 messages. " +
			"Format the subject as `<type>[optional scope][!]: <description>` where type is one of " +
			"feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert " +
			"(`feat` correlates with MINOR, `fix` with PATCH per SemVer). " +
			"Description is imperative, lowercase, no trailing period; the whole subject line stays under 72 chars. " +
			"After one blank line, an optional body explains motivation (the WHY). " +
			"Optional footers use `Token: value` form (use `-` not space in tokens, except `BREAKING CHANGE`). " +
			"Signal breaking changes with `!` before the colon and/or a `BREAKING CHANGE: <description>` footer. " +
			"Output only the commit message — no preamble, no code fences."
	default:
		return "You write concise git commit messages. " +
			"Style: imperative subject under 72 chars, optional body explaining WHY (not WHAT) after a blank line. " +
			"Output only the commit message — no preamble, no code fences."
	}
}

func formatMessage(m history.Message, maxChars int) string {
	text := strings.TrimSpace(m.Text)
	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars] + "…"
	}
	return fmt.Sprintf("[%s] %s: %s\n", m.Time.Local().Format("15:04:05"), m.Role, text)
}

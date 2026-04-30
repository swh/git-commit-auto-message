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

// cliFlags is the parsed command-line flag set. Held in one struct so we
// don't thread eight pointers through every helper.
type cliFlags struct {
	model         string
	printOnly     bool
	noHistory     bool
	fallbackN     int
	maxMsgChar    int
	maxBucketChar int
	style         string
	hints         []string
}

func parseFlags() cliFlags {
	var f cliFlags
	pflag.StringVarP(&f.model, "model", "m", "", "override model id passed to `claude --model`")
	pflag.BoolVarP(&f.printOnly, "print", "p", false, "print the message and exit; do not prompt or commit")
	pflag.BoolVarP(&f.noHistory, "no-history", "n", false, "skip Claude Code conversation history")
	pflag.IntVar(&f.fallbackN, "fallback-messages", 20, "max recent messages to include when no per-file correlation matches")
	pflag.IntVar(&f.maxMsgChar, "max-message-chars", 800, "truncate each transcript message to this many chars")
	pflag.IntVar(&f.maxBucketChar, "max-bucket-chars", 8000, "max chars of transcript per file; oldest messages dropped to fit")
	pflag.StringVar(&f.style, "style", "", "commit message style: traditional|conventional (overrides config)")
	pflag.StringArrayVarP(&f.hints, "hint", "H", nil, "extra context for the model (repeatable, e.g. -H 'fixes #123' -H 'preparing for v2 release')")
	pflag.Parse()
	return f
}

func run() error {
	flags := parseFlags()

	cwd, root, err := repoContext()
	if err != nil {
		return err
	}

	interactive := !flags.printOnly &&
		isatty.IsTerminal(os.Stdin.Fd()) &&
		isatty.IsTerminal(os.Stderr.Fd())
	resolved, err := config.Resolve(config.Style(flags.style), root, interactive, ui.ChooseStyle)
	if err != nil {
		return err
	}
	if !flags.printOnly {
		fmt.Fprintf(os.Stderr, "gcam: style=%s (%s)\n", resolved.Style, resolved.Source)
	}

	files, m, err := selectFiles(cwd, flags.printOnly)
	if err != nil {
		return err
	}
	if files == nil {
		fmt.Fprintln(os.Stderr, "cancelled")
		return nil
	}
	if !flags.printOnly {
		announceMode(cwd, files, m)
	}

	diff, err := buildDiff(cwd, m)
	if err != nil {
		return err
	}

	buckets, fallback := gatherTranscript(root, files, flags)
	prompt := buildPrompt(cwd, diff, buckets, fallback, flags.maxMsgChar, resolved.Style, flags.hints)

	msg, err := suggestMessage(context.Background(), prompt, flags.model)
	if err != nil {
		return err
	}

	if flags.printOnly {
		fmt.Println(msg)
		return nil
	}
	return interactiveLoop(cwd, files, m, msg)
}

// repoContext resolves the cwd and the enclosing git repo root.
func repoContext() (cwd, root string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return "", "", err
	}
	root, err = git.RepoRoot(cwd)
	if err != nil {
		return "", "", fmt.Errorf("not in a git repo: %w", err)
	}
	return cwd, root, nil
}

// selectFiles lists changed files under cwd, splits them into staged vs
// other, and asks the user (or auto-decides) which set to commit. Returns
// (nil, _, nil) when the user cancels at the stage-mode prompt.
func selectFiles(cwd string, printOnly bool) ([]git.ChangedFile, mode, error) {
	all, err := git.ChangedFiles(cwd)
	if err != nil {
		return nil, 0, fmt.Errorf("git status: %w", err)
	}
	if len(all) == 0 {
		return nil, 0, errors.New("no changes detected in current directory or below")
	}

	var staged, other []git.ChangedFile
	for _, f := range all {
		if f.IsStaged() {
			staged = append(staged, f)
		}
		if f.HasUnstaged() {
			other = append(other, f)
		}
	}
	return chooseMode(cwd, all, staged, other, printOnly)
}

// buildDiff returns the diff that will be sent to the model — either the
// staged-only diff or the full cwd-scoped diff (tracked + untracked
// synthetic). Errors out if the chosen set has no diff content.
func buildDiff(cwd string, m mode) (string, error) {
	var (
		diff string
		err  error
	)
	if m == modeStagedOnly {
		diff, err = git.DiffStaged(cwd)
	} else {
		diff, err = git.DiffScoped(cwd)
	}
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return "", errors.New("selected set has no diff content")
	}
	return diff, nil
}

// gatherTranscript loads Claude Code transcript messages and correlates them
// with the changed files. Returns (nil, nil) when --no-history is set or no
// transcript exists. Falls back to the last N messages globally when no
// per-file window matched anything.
func gatherTranscript(root string, files []git.ChangedFile, flags cliFlags) ([]fileBucket, []history.Message) {
	if flags.noHistory {
		return nil, nil
	}
	lowers := lowerBounds(root, files)
	since := earliestNonZero(lowers)
	msgs, _ := history.Recent(root, since)
	buckets := correlate(files, lowers, msgs)
	for i := range buckets {
		buckets[i].Messages = compactBucket(buckets[i].Messages, flags.maxMsgChar, flags.maxBucketChar)
	}
	if !anyBucketHasMessages(buckets) {
		return buckets, lastN(msgs, flags.fallbackN)
	}
	return buckets, nil
}

// suggestMessage shells out to `claude -p`, strips any code fences the model
// added despite being told not to, and rejects empty output.
func suggestMessage(ctx context.Context, prompt, model string) (string, error) {
	raw, err := claudecli.Suggest(ctx, prompt, model)
	if err != nil {
		return "", err
	}
	msg := stripCodeFences(strings.TrimSpace(raw))
	if msg == "" {
		return "", fmt.Errorf("claude returned an empty message (raw output: %q)", raw)
	}
	return msg, nil
}

// interactiveLoop shows the suggestion to the user and loops on edit until
// they accept or cancel. On accept, the commit is performed.
func interactiveLoop(cwd string, files []git.ChangedFile, m mode, msg string) error {
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
		choice, err := ui.ChooseStageMode(cwd, staged, other)
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
	fmt.Fprint(os.Stderr, ui.RenderFileList(cwd, files))
	fmt.Fprintln(os.Stderr)
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
	sizes := make([]int, len(msgs))
	total := 0
	for i, m := range msgs {
		sizes[i] = len(formatMessage(m, maxMsgChars))
		total += sizes[i]
	}
	for total > maxBucketChars && len(msgs) > 0 {
		total -= sizes[0]
		msgs, sizes = msgs[1:], sizes[1:]
	}
	return msgs
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
	cleaned := cleanHints(hints)

	var b strings.Builder
	b.WriteString(systemInstruction(style))
	if len(cleaned) > 0 {
		b.WriteString(" When the user supplies hints (see below), you MUST reflect each hint in the commit message — usually in the body, or in the subject if the hint is the main reason for the change. Treat hints as overriding any conflicting signal from the diff or transcript.")
	}
	b.WriteString("\n\n")

	if len(cleaned) > 0 {
		b.WriteString("# REQUIRED HINTS FROM THE USER — reflect every one of these in the commit message you produce\n")
		for _, line := range cleaned {
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

	if len(cleaned) > 0 {
		b.WriteString("\n# Final reminder\n")
		b.WriteString("Before producing the commit message, double-check that EVERY hint above is reflected in your output. Hints carry context the diff alone cannot show; omitting them is a failure of the task.\n")
		for _, line := range cleaned {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}

	return b.String()
}

// stripCodeFences extracts the commit message from a fenced code block, if
// the model emitted one. The system instruction forbids fences but the
// model occasionally adds them anyway, sometimes with preamble like
// "Here's the commit message:" above. We find the first opening fence and
// the last closing fence and return the content between them. If fences
// aren't balanced, we leave the input intact rather than mangle it.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	openIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			openIdx = i
			break
		}
	}
	if openIdx < 0 {
		return s
	}
	closeIdx := -1
	for i := len(lines) - 1; i > openIdx; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return s
	}
	return strings.TrimSpace(strings.Join(lines[openIdx+1:closeIdx], "\n"))
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

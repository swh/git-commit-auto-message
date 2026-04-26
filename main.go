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

	"github.com/spf13/pflag"

	"github.com/swh/git-commit-auto-message/internal/claudecli"
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
		model      = pflag.StringP("model", "m", "", "override model id passed to `claude --model`")
		printOnly  = pflag.BoolP("print", "p", false, "print the message and exit; do not prompt or commit")
		noHistory  = pflag.BoolP("no-history", "n", false, "skip Claude Code conversation history")
		windowMin  = pflag.IntP("window", "w", 5, "minutes ± a file's mtime to pull related transcript messages")
		fallbackN  = pflag.Int("fallback-messages", 20, "max recent messages to include when no per-file correlation matches")
		maxMsgChar = pflag.Int("max-message-chars", 800, "truncate each transcript message to this many chars")
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

	window := time.Duration(*windowMin) * time.Minute
	var buckets []fileBucket
	var fallback []history.Message
	if !*noHistory {
		earliest := earliestMtime(files)
		var since time.Time
		if !earliest.IsZero() {
			since = earliest.Add(-window - 30*time.Minute)
		}
		msgs, _ := history.Recent(root, since)
		buckets = correlate(files, msgs, window)
		if !anyBucketHasMessages(buckets) {
			fallback = lastN(msgs, *fallbackN)
		}
	}

	prompt := buildPrompt(cwd, diff, buckets, fallback, window, *maxMsgChar)

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

// fileBucket pairs a changed file with the transcript messages timestamped
// near its mtime — the model's best guess at what drove the change.
type fileBucket struct {
	File     git.ChangedFile
	Messages []history.Message
}

func correlate(files []git.ChangedFile, msgs []history.Message, window time.Duration) []fileBucket {
	buckets := make([]fileBucket, 0, len(files))
	for _, f := range files {
		if f.Mtime.IsZero() {
			buckets = append(buckets, fileBucket{File: f})
			continue
		}
		var related []history.Message
		for _, m := range msgs {
			d := m.Time.Sub(f.Mtime)
			if d < 0 {
				d = -d
			}
			if d <= window {
				related = append(related, m)
			}
		}
		sort.Slice(related, func(i, j int) bool { return related[i].Time.Before(related[j].Time) })
		buckets = append(buckets, fileBucket{File: f, Messages: related})
	}
	return buckets
}

func anyBucketHasMessages(buckets []fileBucket) bool {
	for _, b := range buckets {
		if len(b.Messages) > 0 {
			return true
		}
	}
	return false
}

func earliestMtime(files []git.ChangedFile) time.Time {
	var t time.Time
	for _, f := range files {
		if f.Mtime.IsZero() {
			continue
		}
		if t.IsZero() || f.Mtime.Before(t) {
			t = f.Mtime
		}
	}
	return t
}

func lastN(msgs []history.Message, n int) []history.Message {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

func buildPrompt(cwd, diff string, buckets []fileBucket, fallback []history.Message, window time.Duration, maxChars int) string {
	var b strings.Builder
	b.WriteString("You write concise git commit messages. Style: imperative subject under 72 chars, optional body explaining WHY (not WHAT) after a blank line. Output only the commit message — no preamble, no code fences.\n\n")

	b.WriteString("# Diff (scoped to the user's current directory and below)\n")
	b.WriteString("```diff\n")
	b.WriteString(diff)
	b.WriteString("\n```\n")

	if len(buckets) > 0 && anyBucketHasMessages(buckets) {
		fmt.Fprintf(&b, "\n# Likely intent — transcript messages within ±%s of each file's mtime\n", window)
		b.WriteString("Use these to infer *why* the change was made, not *what*.\n")
		for _, bk := range buckets {
			if len(bk.Messages) == 0 {
				continue
			}
			rel, err := filepath.Rel(cwd, bk.File.Path)
			if err != nil {
				rel = bk.File.Path
			}
			fmt.Fprintf(&b, "\n## %s (modified %s)\n", rel, bk.File.Mtime.Local().Format("2006-01-02 15:04:05"))
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

func formatMessage(m history.Message, maxChars int) string {
	text := strings.TrimSpace(m.Text)
	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars] + "…"
	}
	return fmt.Sprintf("[%s] %s: %s\n", m.Time.Local().Format("15:04:05"), m.Role, text)
}

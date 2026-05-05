// Package ui prompts the user to accept, edit, or cancel a suggested commit
// message. Edit opens $EDITOR (falling back to vi) on a temp file.
package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"

	"github.com/swh/git-commit-auto-message/internal/config"
	"github.com/swh/git-commit-auto-message/internal/git"
)

// RenderFileList formats a set of changed files as a short, indented status
// listing — one line per file, paths shown relative to cwd. Used both by the
// "Will commit X file(s)" announcement and by the stage-mode prompt.
func RenderFileList(cwd string, files []git.ChangedFile) string {
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

type Action int

const (
	Cancel Action = iota
	Accept
	Edit
)

var msgBox = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(0, 1)

// boxedMessage renders the suggestion inside the styled box, capping the
// content width to fit the current terminal so long lines wrap inside the
// border instead of bleeding past the right edge. Lipgloss adds 4 columns of
// chrome (border + padding) on top of the content width; huh indents the
// description a few more, so we leave extra slack.
func boxedMessage(s string) string {
	const chrome = 4 // border + padding columns
	const huhIndent = 4
	width := max(terminalWidth()-chrome-huhIndent, 20)
	return msgBox.Width(width).Render(s)
}

// terminalWidth returns the current terminal width, or 80 if it can't be
// determined (e.g. piped output or a non-TTY parent).
func terminalWidth() int {
	for _, fd := range []uintptr{os.Stderr.Fd(), os.Stdout.Fd()} {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// Confirm prints the suggestion and shows a select prompt. Default selection
// is Cancel — Ctrl+C / Esc also cancels.
func Confirm(suggested string) (string, Action, error) {
	box := boxedMessage(suggested)
	var action Action
	err := huh.NewSelect[Action]().
		Title("Commit this message?").
		Description(box).
		Options(
			huh.NewOption("Cancel", Cancel),
			huh.NewOption("Accept and commit", Accept),
			huh.NewOption("Edit in $EDITOR", Edit),
		).
		Value(&action).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", Cancel, nil
		}
		return "", Cancel, err
	}

	switch action {
	case Accept:
		return suggested, Accept, nil
	case Edit:
		edited, err := openEditor(suggested)
		if err != nil {
			return "", Cancel, err
		}
		edited = strings.TrimSpace(edited)
		if edited == "" {
			return "", Cancel, nil
		}
		return edited, Edit, nil
	default:
		return "", Cancel, nil
	}
}

// ChooseStyle is the first-run prompt asking which commit-message style the
// user wants. Returns an error (via huh.ErrUserAborted) if the user
// dismisses the prompt.
func ChooseStyle() (config.Style, error) {
	desc := "Saved to ~/.config/gcam/config.json. Override per-repo with .gcam.json or per-run with --style."
	var style config.Style
	err := huh.NewSelect[config.Style]().
		Title("Choose your commit message style").
		Description(desc).
		Options(
			huh.NewOption("Conventional Commits (feat: …, fix: …, BREAKING CHANGE)", config.StyleConventional),
			huh.NewOption("Traditional (free-form imperative subject + body)", config.StyleTraditional),
		).
		Value(&style).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", errors.New("style selection cancelled")
		}
		return "", err
	}
	return style, nil
}

// StageChoice is the user's answer to the "what to commit" prompt when the
// cwd contains both pre-staged files and other (unstaged or untracked) files.
type StageChoice int

const (
	ChoiceCancel StageChoice = iota
	ChoiceStagedOnly
	ChoiceStageAll
)

// ChooseStageMode is shown when cwd has both pre-staged and other changes.
// staged and other are rendered as indented file listings (relative to cwd).
// Default selection is Cancel.
func ChooseStageMode(cwd string, staged, other []git.ChangedFile) (StageChoice, error) {
	desc := "Already staged:\n" + strings.TrimRight(RenderFileList(cwd, staged), "\n") +
		"\n\nOther changes:\n" + strings.TrimRight(RenderFileList(cwd, other), "\n")

	var choice StageChoice
	err := huh.NewSelect[StageChoice]().
		Title("Some files are already staged — what would you like to commit?").
		Description(desc).
		Options(
			huh.NewOption("Cancel", ChoiceCancel),
			huh.NewOption("Staged files only", ChoiceStagedOnly),
			huh.NewOption("Stage everything and commit", ChoiceStageAll),
		).
		Value(&choice).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ChoiceCancel, nil
		}
		return ChoiceCancel, err
	}
	return choice, nil
}

func openEditor(initial string) (string, error) {
	f, err := os.CreateTemp("", "COMMIT_EDITMSG-*.txt")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	if _, err := f.WriteString(initial); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	editor := os.Getenv("GIT_EDITOR")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command("sh", "-c", editor+" \""+path+"\"")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Package ui prompts the user to accept, edit, or cancel a suggested commit
// message. Edit opens $EDITOR (falling back to vi) on a temp file.
package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/swh/git-commit-auto-message/internal/config"
)

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

// Confirm prints the suggestion and shows a select prompt. Default selection
// is Cancel — Ctrl+C / Esc also cancels.
func Confirm(suggested string) (string, Action, error) {
	box := msgBox.Render(suggested)
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

// ChooseStageMode is shown when the cwd has both pre-staged and other
// changes. The two summaries are shown verbatim (callers format them).
// Default selection is Cancel.
func ChooseStageMode(stagedSummary, otherSummary string) (StageChoice, error) {
	desc := "Already staged:\n" + strings.TrimRight(stagedSummary, "\n") +
		"\n\nOther changes:\n" + strings.TrimRight(otherSummary, "\n")

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

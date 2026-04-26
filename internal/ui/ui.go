// Package ui prompts the user to accept, edit, or cancel a suggested commit
// message. Edit opens $EDITOR (falling back to vi) on a temp file.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Action int

const (
	Cancel Action = iota
	Accept
	Edit
)

// Confirm prints the suggestion, prompts for a/e/c, and returns the final
// message plus the chosen action. Default (empty input) is Cancel — pressing
// enter at the prompt should never silently commit.
func Confirm(suggested string) (string, Action, error) {
	fmt.Fprintln(os.Stderr, "─── suggested commit message ───")
	fmt.Fprintln(os.Stderr, suggested)
	fmt.Fprintln(os.Stderr, "────────────────────────────────")
	fmt.Fprint(os.Stderr, "[a]ccept / [e]dit / [C]ancel? ")

	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", Cancel, err
	}
	choice := strings.ToLower(strings.TrimSpace(line))

	switch choice {
	case "a", "y", "yes", "accept":
		return suggested, Accept, nil
	case "e", "edit":
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

// StageChoice is the user's answer to the "what to commit" prompt when the
// cwd contains both pre-staged files and other (unstaged or untracked) files.
type StageChoice int

const (
	ChoiceCancel StageChoice = iota
	ChoiceStagedOnly
	ChoiceStageAll
)

// ChooseStageMode is shown when the cwd has both pre-staged and other
// changes. The two summaries are printed verbatim (callers format them).
// Default (empty input) is Cancel.
func ChooseStageMode(stagedSummary, otherSummary string) (StageChoice, error) {
	fmt.Fprintln(os.Stderr, "Some files are already staged.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Already staged:")
	fmt.Fprint(os.Stderr, stagedSummary)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Other changes:")
	fmt.Fprint(os.Stderr, otherSummary)
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "[s]taged only / [a]dd others too / [C]ancel? ")

	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ChoiceCancel, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "staged":
		return ChoiceStagedOnly, nil
	case "a", "add", "all":
		return ChoiceStageAll, nil
	default:
		return ChoiceCancel, nil
	}
}

func openEditor(initial string) (string, error) {
	f, err := os.CreateTemp("", "COMMIT_EDITMSG-*.txt")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer os.Remove(path)

	if _, err := f.WriteString(initial); err != nil {
		f.Close()
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

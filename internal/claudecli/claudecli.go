// Package claudecli runs the local `claude` CLI in non-interactive mode to
// generate a commit message suggestion.
package claudecli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Suggest pipes prompt to `claude -p` and returns its stdout.
// model may be empty to use the CLI's configured default.
func Suggest(ctx context.Context, prompt, model string) (string, error) {
	args := []string{"-p"}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("`claude` CLI not found in PATH — install Claude Code (https://claude.com/claude-code)")
		}
		cmdline := "claude " + strings.Join(args, " ")
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = strings.TrimSpace(stdout.String())
		}
		if details == "" {
			return "", fmt.Errorf("%s: %w (no output on stdout/stderr — try running `claude -p hello` to check that it's installed and authenticated)", cmdline, err)
		}
		return "", fmt.Errorf("%s: %w\n%s", cmdline, err, details)
	}
	return strings.TrimSpace(stdout.String()), nil
}

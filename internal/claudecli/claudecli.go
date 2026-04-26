// Package claudecli runs the local `claude` CLI in non-interactive mode to
// generate a commit message suggestion.
package claudecli

import (
	"bytes"
	"context"
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
		return "", fmt.Errorf("claude %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

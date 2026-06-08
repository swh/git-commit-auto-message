// Command uitest drives ui.Confirm with a long, multi-line message so the
// scroll-above-the-menu behaviour can be eyeballed in a real terminal.
// Throwaway harness — delete when done. Run: go run ./cmd/uitest
package main

import (
	"fmt"
	"strings"

	"github.com/swh/git-commit-auto-message/internal/ui"
)

func main() {
	var b strings.Builder
	b.WriteString("feat: a deliberately enormous commit message to test scrolling\n\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "- body line %02d: lorem ipsum dolor sit amet, consectetur adipiscing elit\n", i)
	}

	msg, action, err := ui.Confirm(b.String())
	fmt.Printf("\naction=%d err=%v\nmsg=%q\n", action, err, msg)
}

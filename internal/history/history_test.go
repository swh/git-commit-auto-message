package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecent_SkipsGcamGeneratedSessions verifies that session files whose
// first user message is a gcam-generated prompt are dropped — otherwise gcam
// would pull its own prior prompts back in as "user intent" on every run.
func TestRecent_SkipsGcamGeneratedSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := "/tmp/some-repo"
	dir := filepath.Join(home, ".claude", "projects", "-tmp-some-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A gcam-generated session: first user message starts with the sentinel.
	gcamSess := `{"type":"user","timestamp":"2026-05-05T10:00:00.000Z","message":{"role":"user","content":"<!-- gcam:auto-prompt -->\nYou write Conventional Commits..."}}
{"type":"assistant","timestamp":"2026-05-05T10:00:01.000Z","message":{"role":"assistant","content":"feat: do a thing"}}
`
	// A real interactive session.
	realSess := `{"type":"user","timestamp":"2026-05-05T11:00:00.000Z","message":{"role":"user","content":"please refactor the foo function"}}
{"type":"assistant","timestamp":"2026-05-05T11:00:01.000Z","message":{"role":"assistant","content":"sure, here's the plan"}}
`
	if err := os.WriteFile(filepath.Join(dir, "gcam.jsonl"), []byte(gcamSess), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.jsonl"), []byte(realSess), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, err := Recent(root, time.Time{})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (only the real session): %+v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.Text == "" {
			t.Errorf("empty text in message: %+v", m)
		}
		if m.Time.Year() != 2026 || m.Time.Month() != 5 || m.Time.Day() != 5 {
			t.Errorf("unexpected time: %v", m.Time)
		}
	}
	if msgs[0].Text != "please refactor the foo function" {
		t.Errorf("first kept message wrong: %q", msgs[0].Text)
	}
}

// TestRecent_SentinelOnlyAtStart confirms that a session whose user message
// merely *mentions* the sentinel mid-text (e.g. someone discussing gcam in a
// real conversation) is NOT skipped.
func TestRecent_SentinelOnlyAtStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := "/tmp/some-repo"
	dir := filepath.Join(home, ".claude", "projects", "-tmp-some-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	mention := `{"type":"user","timestamp":"2026-05-05T12:00:00.000Z","message":{"role":"user","content":"the sentinel is <!-- gcam:auto-prompt --> by the way"}}
`
	if err := os.WriteFile(filepath.Join(dir, "mention.jsonl"), []byte(mention), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, err := Recent(root, time.Time{})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (mention should not trigger filter)", len(msgs))
	}
}

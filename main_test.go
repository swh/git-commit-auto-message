package main

import (
	"strings"
	"testing"
	"time"

	"github.com/swh/git-commit-auto-message/internal/config"
	"github.com/swh/git-commit-auto-message/internal/git"
	"github.com/swh/git-commit-auto-message/internal/history"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

func TestCleanHints(t *testing.T) {
	got := cleanHints([]string{"  fixes #1 ", "", "  ", "preparing v2", "\n"})
	want := []string{"fixes #1", "preparing v2"}
	if len(got) != len(want) {
		t.Fatalf("len: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}

	if h := cleanHints(nil); len(h) != 0 {
		t.Errorf("nil input: got %v, want empty", h)
	}
}

func TestEarliestNonZero(t *testing.T) {
	a := mustTime(t, "2026-04-25T10:00:00Z")
	b := mustTime(t, "2026-04-26T10:00:00Z")
	cases := []struct {
		name string
		in   []time.Time
		want time.Time
	}{
		{"empty", nil, time.Time{}},
		{"all zero", []time.Time{{}, {}}, time.Time{}},
		{"one value", []time.Time{a}, a},
		{"earliest wins", []time.Time{b, a}, a},
		{"zeroes ignored", []time.Time{{}, b, {}, a, {}}, a},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := earliestNonZero(c.in); !got.Equal(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestCorrelate_Window(t *testing.T) {
	t0 := mustTime(t, "2026-04-25T10:00:00Z")
	t1 := mustTime(t, "2026-04-25T10:30:00Z")
	t2 := mustTime(t, "2026-04-25T11:00:00Z")
	t3 := mustTime(t, "2026-04-25T11:30:00Z")
	t4 := mustTime(t, "2026-04-25T12:00:00Z")

	files := []git.ChangedFile{
		{Path: "/repo/a.go", Mtime: t3, Status: " M"},
		{Path: "/repo/b.go", Mtime: t4, Status: " M"},
		{Path: "/repo/c.go", Mtime: time.Time{}, Status: " D"}, // deleted, no mtime
	}
	lowers := []time.Time{t1, time.Time{}, t1} // b has no lower bound

	msgs := []history.Message{
		{Time: t0, Role: "user", Text: "before any window"},
		{Time: t1, Role: "user", Text: "exactly at lower bound"}, // included for a (>=t1) and b (no lower)
		{Time: t2, Role: "user", Text: "mid"},
		{Time: t3, Role: "assistant", Text: "at a's mtime"},      // included for a (<=mtime), included for b
		{Time: t4, Role: "user", Text: "at b's mtime"},           // excluded for a (after mtime), included for b
		{Time: t4.Add(time.Minute), Role: "user", Text: "after"}, // excluded for both
	}

	got := correlate(files, lowers, msgs)
	if len(got) != 3 {
		t.Fatalf("buckets: got %d, want 3", len(got))
	}

	// a: [t1, t3] → t1, t2, t3
	if a := got[0]; len(a.Messages) != 3 ||
		!a.Messages[0].Time.Equal(t1) ||
		!a.Messages[1].Time.Equal(t2) ||
		!a.Messages[2].Time.Equal(t3) {
		t.Errorf("a bucket: got %v", a.Messages)
	}
	if !got[0].Lower.Equal(t1) {
		t.Errorf("a lower: got %v, want %v", got[0].Lower, t1)
	}

	// b: [zero, t4] → all up to and including t4 (t0..t4 = 5 messages)
	if b := got[1]; len(b.Messages) != 5 {
		t.Errorf("b bucket: got %d msgs, want 5: %v", len(b.Messages), b.Messages)
	}

	// c: deleted, no correlation done
	if c := got[2]; len(c.Messages) != 0 {
		t.Errorf("c bucket: got %d msgs, want 0", len(c.Messages))
	}
}

func TestCorrelate_SortedByTime(t *testing.T) {
	tA := mustTime(t, "2026-04-25T10:00:00Z")
	tB := mustTime(t, "2026-04-25T10:05:00Z")
	tC := mustTime(t, "2026-04-25T10:10:00Z")
	files := []git.ChangedFile{{Path: "/repo/x", Mtime: tC.Add(time.Hour)}}
	lowers := []time.Time{tA.Add(-time.Hour)}
	// Out-of-order input
	msgs := []history.Message{
		{Time: tC, Role: "user", Text: "c"},
		{Time: tA, Role: "user", Text: "a"},
		{Time: tB, Role: "user", Text: "b"},
	}
	got := correlate(files, lowers, msgs)
	if len(got[0].Messages) != 3 {
		t.Fatalf("got %d msgs", len(got[0].Messages))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[0].Messages[i].Text != want {
			t.Errorf("[%d]: got %q, want %q", i, got[0].Messages[i].Text, want)
		}
	}
}

func TestCompactBucket(t *testing.T) {
	now := mustTime(t, "2026-04-25T10:00:00Z")
	mk := func(i int) history.Message {
		return history.Message{
			Time: now.Add(time.Duration(i) * time.Minute),
			Role: "user",
			Text: strings.Repeat("x", 100),
		}
	}
	msgs := []history.Message{mk(0), mk(1), mk(2), mk(3), mk(4)}

	// Cap that fits all → no change
	if got := compactBucket(msgs, 800, 100000); len(got) != 5 {
		t.Errorf("no-op cap: got %d, want 5", len(got))
	}

	// Cap that fits ~2 messages → oldest dropped, last 2 kept
	got := compactBucket(msgs, 800, 250)
	if len(got) >= 5 {
		t.Errorf("expected truncation, got %d msgs", len(got))
	}
	// Newest message must always be retained
	if got[len(got)-1].Time != mk(4).Time {
		t.Error("newest message was dropped")
	}
	// Order preserved: each timestamp strictly later than the previous
	for i := 1; i < len(got); i++ {
		if !got[i].Time.After(got[i-1].Time) {
			t.Errorf("order broken at %d", i)
		}
	}

	// maxBucketChars == 0 disables the cap
	if g := compactBucket(msgs, 800, 0); len(g) != 5 {
		t.Errorf("disabled cap: got %d, want 5", len(g))
	}

	// Empty input
	if g := compactBucket(nil, 800, 100); g != nil {
		t.Errorf("nil input: got %v", g)
	}
}

func TestSystemInstruction(t *testing.T) {
	trad := systemInstruction(config.StyleTraditional)
	if !strings.Contains(trad, "imperative subject under 72 chars") {
		t.Errorf("traditional missing key phrase: %q", trad)
	}
	if strings.Contains(trad, "Conventional Commits") {
		t.Error("traditional should not mention Conventional Commits")
	}

	conv := systemInstruction(config.StyleConventional)
	if !strings.Contains(conv, "Conventional Commits 1.0.0") {
		t.Errorf("conventional missing version reference: %q", conv)
	}
	for _, kw := range []string{"feat", "fix", "BREAKING CHANGE", "<type>"} {
		if !strings.Contains(conv, kw) {
			t.Errorf("conventional missing %q", kw)
		}
	}
}

func TestBuildPrompt_HintsSection(t *testing.T) {
	out := buildPrompt("/repo", "diff content", nil, nil, 800, config.StyleTraditional, []string{"fixes #1", "for v2"})

	// System-instruction addendum referencing hints.
	if !strings.Contains(out, "MUST reflect each hint") {
		t.Error("system instruction should mention the hint requirement")
	}

	// Required-hints section near the top, before the diff.
	if !strings.Contains(out, "# REQUIRED HINTS FROM THE USER") {
		t.Error("required-hints section header missing")
	}
	if !strings.Contains(out, "- fixes #1") || !strings.Contains(out, "- for v2") {
		t.Errorf("hint bullets missing: %s", out)
	}
	if strings.Index(out, "# REQUIRED HINTS") > strings.Index(out, "# Diff") {
		t.Error("hints should appear before the diff")
	}

	// Trailing reminder after the diff/transcript section.
	reminderIdx := strings.Index(out, "# Final reminder")
	diffIdx := strings.Index(out, "# Diff")
	if reminderIdx < 0 {
		t.Error("trailing reminder section missing")
	}
	if reminderIdx < diffIdx {
		t.Error("final reminder should appear after the diff")
	}

	// No hints → neither section, no system addendum.
	out2 := buildPrompt("/repo", "diff content", nil, nil, 800, config.StyleTraditional, nil)
	if strings.Contains(out2, "# REQUIRED HINTS") || strings.Contains(out2, "# Final reminder") {
		t.Error("hint sections should be omitted when no hints supplied")
	}
	if strings.Contains(out2, "MUST reflect each hint") {
		t.Error("hint addendum should not appear when no hints supplied")
	}

	// Empty/whitespace hints are filtered out.
	out3 := buildPrompt("/repo", "diff", nil, nil, 800, config.StyleTraditional, []string{"  ", ""})
	if strings.Contains(out3, "# REQUIRED HINTS") || strings.Contains(out3, "# Final reminder") {
		t.Error("whitespace-only hints should not produce sections")
	}
}

func TestStripCodeFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no fences", "feat: x\n\nbody", "feat: x\n\nbody"},
		{"plain fence", "```\nfeat: x\n\nbody\n```", "feat: x\n\nbody"},
		{"language tag", "```text\nfeat: x\n```", "feat: x"},
		{"trailing whitespace around fence", "  ```\nfeat: x\n```  \n", "feat: x"},
		{"unbalanced (no trailing fence) — leave intact", "```\nfeat: x\nbody", "```\nfeat: x\nbody"},
		{"single line wrapped — too ambiguous, leave intact", "```feat: x```", "```feat: x```"},
		{"empty input", "", ""},
		{"fences only", "```\n```", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripCodeFences(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLastN(t *testing.T) {
	mk := func(i int) history.Message {
		return history.Message{Time: time.Unix(int64(i), 0), Text: "x"}
	}
	all := []history.Message{mk(1), mk(2), mk(3), mk(4)}

	if got := lastN(all, 10); len(got) != 4 {
		t.Errorf("n>=len: got %d, want 4", len(got))
	}
	if got := lastN(all, 2); len(got) != 2 || got[0].Time.Unix() != 3 {
		t.Errorf("n=2: got %v", got)
	}
	if got := lastN(nil, 5); len(got) != 0 {
		t.Errorf("nil input: got %v", got)
	}
}

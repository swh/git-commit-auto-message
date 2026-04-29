package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// initRepo seeds a fresh git repo at the path returned. user.email/name are
// set locally so commits don't fail in test environments. The returned path
// is run through EvalSymlinks so it matches what `git rev-parse
// --show-toplevel` returns on macOS (where /var resolves to /private/var).
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = resolved
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return resolved
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// gitCommitAt commits with a fixed author + committer date so successive
// commits in a test have monotonically increasing %cI timestamps even when
// they execute within the same wall-clock second.
func gitCommitAt(t *testing.T, dir, msg string, when time.Time) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-q", "-m", msg)
	cmd.Dir = dir
	iso := when.Format(time.RFC3339)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+iso,
		"GIT_COMMITTER_DATE="+iso)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pathSet collects the absolute path of each ChangedFile.
func pathSet(files []ChangedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

// TestChangedFiles_FromSubdirResolvesPaths is a regression test: when run
// from a subdirectory, both tracked and untracked files reported by git
// should resolve to absolute paths *inside* the subdirectory, not at the
// repo root. (Previously, ls-files paths were cwd-relative but joined onto
// the repo root, mis-locating untracked files.)
func TestChangedFiles_FromSubdirResolvesPaths(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")

	// Initial committed state.
	write(t, filepath.Join(repo, "root.txt"), "root\n")
	write(t, filepath.Join(sub, "tracked.txt"), "tracked\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-q", "-m", "init")

	// Modifications + new untracked files.
	write(t, filepath.Join(sub, "tracked.txt"), "modified\n")
	write(t, filepath.Join(sub, "untracked.txt"), "new\n")
	write(t, filepath.Join(sub, "nested", "deep.txt"), "deep\n")
	// File at repo root that should NOT appear when running from sub.
	write(t, filepath.Join(repo, "root-untracked.txt"), "outside\n")

	files, err := ChangedFiles(sub)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	wantTracked := filepath.Join(sub, "tracked.txt")
	wantUntracked := filepath.Join(sub, "untracked.txt")
	wantDeep := filepath.Join(sub, "nested", "deep.txt")
	excluded := filepath.Join(repo, "root-untracked.txt")

	got := pathSet(files)
	want := []string{wantDeep, wantTracked, wantUntracked}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}

	for _, f := range files {
		if f.Path == excluded {
			t.Errorf("file outside cwd leaked into result: %s", f.Path)
		}
	}
}

func TestRepoRoot(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{repo, sub} {
		got, err := RepoRoot(dir)
		if err != nil {
			t.Fatalf("RepoRoot(%s): %v", dir, err)
		}
		if got != repo {
			t.Errorf("from %s: got %q, want %q", dir, got, repo)
		}
	}
}

func TestLastCommitTimeAndHeadTime(t *testing.T) {
	repo := initRepo(t)

	// No HEAD yet.
	if _, ok, err := HeadTime(repo); err != nil || ok {
		t.Errorf("HeadTime on empty repo: ok=%v err=%v, want (false, nil)", ok, err)
	}
	if _, ok, err := LastCommitTime(repo, "anything.txt"); err != nil || ok {
		t.Errorf("LastCommitTime on empty repo: ok=%v err=%v, want (false, nil)", ok, err)
	}

	// First commit at a fixed time so subsequent commits have a strictly
	// later timestamp regardless of test execution speed.
	t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	write(t, filepath.Join(repo, "a.txt"), "a\n")
	gitCmd(t, repo, "add", "a.txt")
	gitCommitAt(t, repo, "first", t1)

	// Untracked file post-first-commit → still no last commit time.
	write(t, filepath.Join(repo, "untracked.txt"), "u\n")
	if _, ok, err := LastCommitTime(repo, "untracked.txt"); err != nil || ok {
		t.Errorf("LastCommitTime untracked: ok=%v err=%v, want (false, nil)", ok, err)
	}

	headT, ok, err := HeadTime(repo)
	if err != nil || !ok {
		t.Fatalf("HeadTime after commit: ok=%v err=%v", ok, err)
	}
	if !headT.Equal(t1) {
		t.Errorf("HEAD time: got %v, want %v", headT, t1)
	}
	aT, ok, err := LastCommitTime(repo, "a.txt")
	if err != nil || !ok {
		t.Fatalf("LastCommitTime: ok=%v err=%v", ok, err)
	}
	if !aT.Equal(t1) {
		t.Errorf("a's last commit time: got %v, want %v", aT, t1)
	}

	// Second commit touching only b.txt; a's commit time must NOT change.
	write(t, filepath.Join(repo, "b.txt"), "b\n")
	gitCmd(t, repo, "add", "b.txt")
	gitCommitAt(t, repo, "second", t2)

	newHead, _, _ := HeadTime(repo)
	if !newHead.Equal(t2) {
		t.Errorf("new HEAD time: got %v, want %v", newHead, t2)
	}
	if !newHead.After(headT) {
		t.Errorf("HEAD time should advance: old=%v new=%v", headT, newHead)
	}

	aT2, _, _ := LastCommitTime(repo, "a.txt")
	if !aT2.Equal(t1) {
		t.Errorf("a's last commit time changed unexpectedly: was %v, now %v", t1, aT2)
	}

	bT, ok, _ := LastCommitTime(repo, "b.txt")
	if !ok || !bT.Equal(t2) {
		t.Errorf("b's last commit time: ok=%v t=%v, want %v", ok, bT, t2)
	}
}

package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RepoRoot returns the absolute path of the git repo containing dir.
func RepoRoot(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ChangedFile is one file reported by `git status --porcelain`. Status is the
// raw 2-char code (e.g. " M", "M ", "MM", "A ", "??", "D ", "R "). Mtime is
// zero for files that no longer exist on disk (deletions).
type ChangedFile struct {
	Path   string // absolute
	Mtime  time.Time
	Status string
}

// IsStaged reports whether the index column shows a change (anything other
// than space or '?').
func (f ChangedFile) IsStaged() bool {
	return len(f.Status) > 0 && f.Status[0] != ' ' && f.Status[0] != '?'
}

// HasUnstaged reports whether the file has working-tree changes or is
// untracked. An "MM" file is both staged and has working-tree changes.
func (f ChangedFile) HasUnstaged() bool {
	if f.Status == "??" {
		return true
	}
	return len(f.Status) > 1 && f.Status[1] != ' '
}

// ChangedFiles returns the modified, staged, and untracked files under `dir`,
// each with its current mtime and porcelain status code. Renames report the
// destination path. Untracked directories are expanded into their individual
// (non-ignored) files via `git ls-files`, so callers see one entry per file.
func ChangedFiles(dir string) ([]ChangedFile, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return nil, err
	}

	// Tracked changes only — untracked are listed separately so we can
	// expand untracked directories into individual files.
	porcelain, err := run(dir, "-c", "core.quotepath=false", "status", "--porcelain", "--untracked-files=no", "--", ".")
	if err != nil {
		return nil, err
	}
	var files []ChangedFile
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(porcelain, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		status := line[:2]
		path := line[3:]
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		abs := filepath.Join(root, path)
		var mtime time.Time
		if info, err := os.Stat(abs); err == nil {
			mtime = info.ModTime()
		}
		files = append(files, ChangedFile{Path: abs, Mtime: mtime, Status: status})
	}

	// Untracked, expanded to individual files (respects .gitignore).
	untracked, err := run(dir, "-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	for _, path := range strings.Split(strings.TrimRight(untracked, "\n"), "\n") {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		abs := filepath.Join(root, path)
		var mtime time.Time
		if info, err := os.Stat(abs); err == nil {
			mtime = info.ModTime()
		}
		files = append(files, ChangedFile{Path: abs, Mtime: mtime, Status: "??"})
	}

	return files, nil
}

// DiffScoped returns the combined diff (staged + unstaged vs HEAD) for `dir` and below,
// plus a synthetic diff for untracked files so the model sees new content. Works
// even before the repo's first commit, when HEAD doesn't yet exist.
func DiffScoped(dir string) (string, error) {
	tracked, err := trackedDiff(dir)
	if err != nil {
		return "", err
	}

	untrackedList, err := run(dir, "ls-files", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(tracked)
	for _, f := range strings.Split(strings.TrimSpace(untrackedList), "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		// Mimic `git diff` for new files so the model sees the contents.
		add, err := run(dir, "diff", "--no-index", "--", "/dev/null", f)
		if err != nil {
			continue
		}
		b.WriteString("\n")
		b.WriteString(add)
	}
	return b.String(), nil
}

// DiffStaged returns the diff of staged content (vs HEAD, or vs the empty
// tree before the first commit) for `dir` and below.
func DiffStaged(dir string) (string, error) {
	if hasHead(dir) {
		return run(dir, "diff", "--cached", "--", ".")
	}
	emptyTree, err := run(dir, "hash-object", "-t", "tree", "/dev/null")
	if err != nil {
		return "", err
	}
	return run(dir, "diff", "--cached", strings.TrimSpace(emptyTree), "--", ".")
}

// trackedDiff returns the diff of tracked files (staged + unstaged) for dir
// and below. Before the first commit there is no HEAD, so we diff staged
// content against the empty tree instead.
func trackedDiff(dir string) (string, error) {
	if hasHead(dir) {
		return run(dir, "diff", "HEAD", "--", ".")
	}
	emptyTree, err := run(dir, "hash-object", "-t", "tree", "/dev/null")
	if err != nil {
		return "", err
	}
	return run(dir, "diff", "--cached", strings.TrimSpace(emptyTree), "--", ".")
}

func hasHead(dir string) bool {
	_, err := run(dir, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// `git diff --no-index` exits 1 when there *are* differences — that's success for us.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && stdout.Len() > 0 {
			return stdout.String(), nil
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

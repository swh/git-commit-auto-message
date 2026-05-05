// Package history reads recent Claude Code conversation transcripts for the
// current project. Claude Code stores per-project sessions as JSONL files at:
//
//	~/.claude/projects/<encoded-path>/<session-uuid>.jsonl
//
// where <encoded-path> is the project root with `/` replaced by `-`.
//
// Each user/assistant entry carries an ISO 8601 `timestamp`, which lets the
// caller correlate transcript messages with file modification times to infer
// intent for individual changes.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Message is a single user or assistant message extracted from a session log.
type Message struct {
	Time time.Time
	Role string // "user" or "assistant"
	Text string
}

// PromptSentinel marks a transcript message as a gcam-generated prompt so we
// can recognise and skip it on subsequent runs. Without this, every `claude
// -p` invocation gets logged into the project's session directory and gets
// fed back as "user intent" on the next run — the model then echoes
// fragments of its own prior prompt back as the suggested commit message.
//
// gcam prepends this sentinel to every prompt; readSession drops any session
// whose first user message begins with it.
const PromptSentinel = "<!-- gcam:auto-prompt -->"

// Recent returns user/assistant messages from session transcripts of the
// project rooted at projectRoot, timestamped at or after `since`. If `since`
// is zero, all messages are returned. The slice is sorted by time. Returns
// nil with no error if the project has no transcripts.
func Recent(projectRoot string, since time.Time) ([]Message, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude", "projects", encode(projectRoot))

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var all []Message
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		// Cheap pre-filter: skip sessions whose file mtime predates `since`.
		// (A session's last-modified time is the latest message time, so if
		// the whole file is older than the window we want, none of its
		// messages can be interesting.)
		if !since.IsZero() {
			info, err := e.Info()
			if err == nil && info.ModTime().Before(since) {
				continue
			}
		}
		msgs, err := readSession(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if since.IsZero() || !m.Time.Before(since) {
				all = append(all, m)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	return all, nil
}

func readSession(path string) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	type entry struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	var out []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	sawFirstUser := false
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			continue
		}
		var m message
		if err := json.Unmarshal(e.Message, &m); err != nil {
			continue
		}
		text := extractText(m.Content)
		if text == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = e.Type
		}
		if !sawFirstUser && role == "user" {
			sawFirstUser = true
			if strings.HasPrefix(strings.TrimSpace(text), PromptSentinel) {
				return nil, nil
			}
		}
		out = append(out, Message{Time: ts, Role: role, Text: text})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// extractText handles both string content and structured content arrays.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

func encode(absPath string) string {
	return strings.ReplaceAll(absPath, "/", "-")
}

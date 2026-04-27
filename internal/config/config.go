// Package config loads and persists the user's commit-message style
// preference. Precedence (highest first):
//   - explicit Style passed to Resolve (e.g. from a CLI flag)
//   - <repo-root>/.gcam.json
//   - $XDG_CONFIG_HOME/gcam/config.json (or ~/.config/gcam/config.json)
//   - interactive first-run prompt (if a TTY is available)
//   - silent default: StyleConventional
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Style string

const (
	StyleTraditional  Style = "traditional"
	StyleConventional Style = "conventional"
)

type Source int

const (
	SourceDefault Source = iota
	SourceUser
	SourceProject
	SourceFlag
	SourceFirstRun
)

func (s Source) String() string {
	switch s {
	case SourceUser:
		return "user"
	case SourceProject:
		return "project"
	case SourceFlag:
		return "flag"
	case SourceFirstRun:
		return "first-run"
	default:
		return "default"
	}
}

type Config struct {
	Style Style `json:"style"`
}

type Resolved struct {
	Style  Style
	Source Source
}

const projectFileName = ".gcam.json"

// UserConfigPath returns $XDG_CONFIG_HOME/gcam/config.json if XDG_CONFIG_HOME
// is set, otherwise $HOME/.config/gcam/config.json. We do not use
// os.UserConfigDir because that returns ~/Library/Application Support on
// macOS — gcam stores config under ~/.config on every platform.
func UserConfigPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gcam", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gcam", "config.json"), nil
}

// ProjectConfigPath returns <repoRoot>/.gcam.json.
func ProjectConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, projectFileName)
}

// LoadUser returns the user-level config, or (nil, nil) if it doesn't exist.
func LoadUser() (*Config, error) {
	path, err := UserConfigPath()
	if err != nil {
		return nil, err
	}
	return loadFile(path)
}

// LoadProject returns the project-level config rooted at repoRoot, or
// (nil, nil) if it doesn't exist or repoRoot is empty.
func LoadProject(repoRoot string) (*Config, error) {
	if repoRoot == "" {
		return nil, nil
	}
	return loadFile(ProjectConfigPath(repoRoot))
}

func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := Validate(c.Style); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// SaveUser atomically writes the user-level config.
func SaveUser(c Config) error {
	if err := Validate(c.Style); err != nil {
		return err
	}
	path, err := UserConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gcam-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Validate returns nil if s is a known style.
func Validate(s Style) error {
	switch s {
	case StyleTraditional, StyleConventional:
		return nil
	}
	return fmt.Errorf("invalid style %q (valid: %s, %s)", string(s), StyleTraditional, StyleConventional)
}

// Resolve walks the precedence chain. flag may be empty (meaning "no flag
// supplied"). repoRoot may be empty (no project file). interactive controls
// whether the first-run prompt may fire; when false and nothing is
// configured, the silent default is StyleConventional.
func Resolve(flag Style, repoRoot string, interactive bool, prompt func() (Style, error)) (Resolved, error) {
	if flag != "" {
		if err := Validate(flag); err != nil {
			return Resolved{}, err
		}
		return Resolved{Style: flag, Source: SourceFlag}, nil
	}
	if proj, err := LoadProject(repoRoot); err != nil {
		return Resolved{}, err
	} else if proj != nil {
		return Resolved{Style: proj.Style, Source: SourceProject}, nil
	}
	if usr, err := LoadUser(); err != nil {
		return Resolved{}, err
	} else if usr != nil {
		return Resolved{Style: usr.Style, Source: SourceUser}, nil
	}
	if interactive && prompt != nil {
		s, err := prompt()
		if err != nil {
			return Resolved{}, err
		}
		if err := Validate(s); err != nil {
			return Resolved{}, err
		}
		if err := SaveUser(Config{Style: s}); err != nil {
			return Resolved{}, fmt.Errorf("save user config: %w", err)
		}
		return Resolved{Style: s, Source: SourceFirstRun}, nil
	}
	return Resolved{Style: StyleConventional, Source: SourceDefault}, nil
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate redirects XDG_CONFIG_HOME and HOME to temp dirs so the test can't
// touch the real ~/.config. Returns the XDG dir.
func isolate(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	return xdg
}

func TestUserConfigPath_XDG(t *testing.T) {
	xdg := isolate(t)
	got, err := UserConfigPath()
	if err != nil {
		t.Fatalf("UserConfigPath: %v", err)
	}
	want := filepath.Join(xdg, "gcam", "config.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUserConfigPath_HomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	got, err := UserConfigPath()
	if err != nil {
		t.Fatalf("UserConfigPath: %v", err)
	}
	want := filepath.Join(home, ".config", "gcam", "config.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestProjectConfigPath(t *testing.T) {
	got := ProjectConfigPath("/tmp/repo")
	want := filepath.Join("/tmp/repo", ".gcam.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		s  Style
		ok bool
	}{
		{StyleTraditional, true},
		{StyleConventional, true},
		{"", false},
		{"Traditional", false},
		{"conv", false},
	}
	for _, c := range cases {
		err := Validate(c.s)
		if (err == nil) != c.ok {
			t.Errorf("Validate(%q) ok=%v, err=%v", c.s, c.ok, err)
		}
	}
}

func TestLoad_MissingReturnsNil(t *testing.T) {
	isolate(t)
	if c, err := LoadUser(); c != nil || err != nil {
		t.Errorf("LoadUser missing: got (%v, %v), want (nil, nil)", c, err)
	}
	repo := t.TempDir()
	if c, err := LoadProject(repo); c != nil || err != nil {
		t.Errorf("LoadProject missing: got (%v, %v), want (nil, nil)", c, err)
	}
	if c, err := LoadProject(""); c != nil || err != nil {
		t.Errorf("LoadProject empty repo: got (%v, %v), want (nil, nil)", c, err)
	}
}

func TestLoadProject_InvalidJSON(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(ProjectConfigPath(repo), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProject(repo); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestLoadProject_InvalidStyle(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(ProjectConfigPath(repo), []byte(`{"style":"sloppy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProject(repo)
	if err == nil || !strings.Contains(err.Error(), "invalid style") {
		t.Errorf("got err=%v, want one mentioning 'invalid style'", err)
	}
}

func TestSaveUser_RoundTrip(t *testing.T) {
	isolate(t)
	if err := SaveUser(Config{Style: StyleConventional}); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := LoadUser()
	if err != nil {
		t.Fatalf("LoadUser: %v", err)
	}
	if got == nil || got.Style != StyleConventional {
		t.Errorf("round-trip: got %+v, want conventional", got)
	}

	path, _ := UserConfigPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("perms: got %o, want 0o644", mode)
	}
}

func TestSaveUser_RejectsInvalid(t *testing.T) {
	isolate(t)
	if err := SaveUser(Config{Style: "junk"}); err == nil {
		t.Error("expected error saving invalid style")
	}
}

func TestResolve_FlagWins(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	if err := os.WriteFile(ProjectConfigPath(repo), []byte(`{"style":"traditional"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveUser(Config{Style: StyleTraditional}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(StyleConventional, repo, true, failingPrompt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Style != StyleConventional || got.Source != SourceFlag {
		t.Errorf("got %+v, want {conventional flag}", got)
	}
}

func TestResolve_FlagInvalid(t *testing.T) {
	isolate(t)
	if _, err := Resolve("nope", "", false, nil); err == nil {
		t.Error("expected error for invalid flag")
	}
}

func TestResolve_ProjectBeatsUser(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	if err := os.WriteFile(ProjectConfigPath(repo), []byte(`{"style":"traditional"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveUser(Config{Style: StyleConventional}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("", repo, true, failingPrompt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Style != StyleTraditional || got.Source != SourceProject {
		t.Errorf("got %+v, want {traditional project}", got)
	}
}

func TestResolve_UserWhenNoProject(t *testing.T) {
	isolate(t)
	if err := SaveUser(Config{Style: StyleTraditional}); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("", t.TempDir(), true, failingPrompt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Style != StyleTraditional || got.Source != SourceUser {
		t.Errorf("got %+v, want {traditional user}", got)
	}
}

func TestResolve_FirstRunInteractive_Persists(t *testing.T) {
	isolate(t)
	prompted := false
	prompt := func() (Style, error) {
		prompted = true
		return StyleConventional, nil
	}
	got, err := Resolve("", t.TempDir(), true, prompt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !prompted {
		t.Error("expected prompt to be called")
	}
	if got.Style != StyleConventional || got.Source != SourceFirstRun {
		t.Errorf("got %+v, want {conventional first-run}", got)
	}
	usr, err := LoadUser()
	if err != nil {
		t.Fatalf("LoadUser: %v", err)
	}
	if usr == nil || usr.Style != StyleConventional {
		t.Errorf("not persisted: got %+v", usr)
	}
}

func TestResolve_NonInteractiveDefault(t *testing.T) {
	isolate(t)
	got, err := Resolve("", t.TempDir(), false, failingPrompt(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Style != StyleConventional || got.Source != SourceDefault {
		t.Errorf("got %+v, want {conventional default}", got)
	}
}

func failingPrompt(t *testing.T) func() (Style, error) {
	t.Helper()
	return func() (Style, error) {
		t.Errorf("prompt should not be called")
		return "", nil
	}
}

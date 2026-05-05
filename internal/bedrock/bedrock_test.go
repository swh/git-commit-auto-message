package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAvailable(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	if Available() {
		t.Error("expected false when env unset")
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "tok")
	if !Available() {
		t.Error("expected true when env set")
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "   ")
	if Available() {
		t.Error("whitespace-only token should count as unset")
	}
}

func TestModel_Precedence(t *testing.T) {
	t.Setenv("GCAM_BEDROCK_MODEL", "")
	if got := Model(""); got != defaultModel {
		t.Errorf("default: got %q, want %q", got, defaultModel)
	}
	t.Setenv("GCAM_BEDROCK_MODEL", "env-model")
	if got := Model(""); got != "env-model" {
		t.Errorf("env wins over default: got %q", got)
	}
	if got := Model("flag-model"); got != "flag-model" {
		t.Errorf("override wins over env: got %q", got)
	}
}

func TestSuggest_HappyPath(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "feat: add bedrock backend\n"},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "tok-123")
	t.Setenv("GCAM_BEDROCK_ENDPOINT", srv.URL)

	out, err := Suggest(context.Background(), "make me a commit message", "")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if out != "feat: add bedrock backend" {
		t.Errorf("got %q", out)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization header: %q", gotAuth)
	}
	// Body should contain the prompt and Anthropic version marker.
	if !strings.Contains(gotBody, "bedrock-2023-05-31") || !strings.Contains(gotBody, "make me a commit message") {
		t.Errorf("unexpected body: %s", gotBody)
	}
}

func TestSuggest_NoToken(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	if _, err := Suggest(context.Background(), "x", ""); err == nil {
		t.Fatal("expected error when token unset")
	}
}

func TestSuggest_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"access denied"}`))
	}))
	defer srv.Close()

	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "tok")
	t.Setenv("GCAM_BEDROCK_ENDPOINT", srv.URL)

	_, err := Suggest(context.Background(), "x", "")
	if err == nil {
		t.Fatal("expected error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should include status and body: %v", err)
	}
}

func TestSuggest_EmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[]}`))
	}))
	defer srv.Close()

	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "tok")
	t.Setenv("GCAM_BEDROCK_ENDPOINT", srv.URL)

	if _, err := Suggest(context.Background(), "x", ""); err == nil {
		t.Fatal("expected error on empty content array")
	}
}

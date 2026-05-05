// Package bedrock invokes Anthropic models on Amazon Bedrock via the
// InvokeModel REST API using bearer-token auth (AWS_BEARER_TOKEN_BEDROCK).
//
// This is gcam's default backend when AWS_BEARER_TOKEN_BEDROCK is set; if the
// token is absent, the caller falls back to `claude -p`. Direct API calls
// also avoid the transcript-poisoning problem that motivates
// history.PromptSentinel — no session JSONL is written.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	envBearer   = "AWS_BEARER_TOKEN_BEDROCK"
	envModel    = "GCAM_BEDROCK_MODEL"
	envEndpoint = "GCAM_BEDROCK_ENDPOINT" // overrides the URL; primarily for tests

	defaultModel  = "global.anthropic.claude-sonnet-4-6"
	defaultRegion = "us-east-1"
)

// Available reports whether AWS_BEARER_TOKEN_BEDROCK is set in the
// environment. Callers use this to decide whether to dispatch to Bedrock.
func Available() bool {
	return strings.TrimSpace(os.Getenv(envBearer)) != ""
}

// Model returns the Bedrock model ID that Suggest will use. Precedence:
// modelOverride > GCAM_BEDROCK_MODEL > built-in default.
func Model(modelOverride string) string {
	if s := strings.TrimSpace(modelOverride); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv(envModel)); s != "" {
		return s
	}
	return defaultModel
}

// Suggest sends the prompt to Bedrock and returns the assistant's text.
// modelOverride may be empty to use the default model id.
func Suggest(ctx context.Context, prompt, modelOverride string) (string, error) {
	token := strings.TrimSpace(os.Getenv(envBearer))
	if token == "" {
		return "", fmt.Errorf("%s not set", envBearer)
	}
	region := firstNonEmpty(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), defaultRegion)
	model := Model(modelOverride)

	body, err := json.Marshal(map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL(region, model), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bedrock invoke: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("bedrock invoke %s: HTTP %d: %s", model, resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 500))
	}

	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("bedrock decode: %w (body: %s)", err, truncate(string(raw), 500))
	}
	var b strings.Builder
	for _, blk := range decoded.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", errors.New("bedrock returned empty content")
	}
	return out, nil
}

func buildURL(region, model string) string {
	if s := strings.TrimSpace(os.Getenv(envEndpoint)); s != "" {
		return s
	}
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke", region, model)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

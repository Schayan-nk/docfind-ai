package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clearChainEnv blanks every DOCFIND_AI_* variable a chain test could pick up
// from the machine running it.
func clearChainEnv(t *testing.T) {
	t.Helper()
	for _, sfx := range []string{"", "_2", "_3", "_4", "_5"} {
		for _, k := range []string{"PROVIDER", "KEY", "MODEL", "URL"} {
			t.Setenv("DOCFIND_AI_"+k+sfx, "")
		}
	}
}

// The user's order since 2026-10-12: Claude, then ChatGPT, then the local
// model. The thing that must not happen is any fallback inheriting the
// Anthropic key or the claude model name.
func TestClaudeFirstThenChatGPTThenLocal(t *testing.T) {
	clearChainEnv(t)
	t.Setenv("DOCFIND_AI_PROVIDER", "anthropic")
	t.Setenv("DOCFIND_AI_KEY", "sk-ant-test-key")
	t.Setenv("DOCFIND_AI_MODEL", "claude-opus-5-5")
	t.Setenv("DOCFIND_AI_PROVIDER_2", "openai")
	t.Setenv("DOCFIND_AI_KEY_2", "sk-proj-openai-key")
	t.Setenv("DOCFIND_AI_MODEL_2", "gpt-5-mini")
	t.Setenv("DOCFIND_AI_PROVIDER_3", "openai")
	t.Setenv("DOCFIND_AI_URL_3", "http://127.0.0.1:11434/v1")
	t.Setenv("DOCFIND_AI_MODEL_3", "docfind-fa")

	chain := ChainFromEnv()
	if len(chain) != 3 {
		t.Fatalf("chain has %d entries, want 3: %+v", len(chain), chain)
	}
	if c := chain[0]; c.Provider != ProviderAnthropic || c.Model != "claude-opus-5-5" || c.BaseURL != "https://api.anthropic.com/v1" {
		t.Errorf("first entry %+v, want Claude on api.anthropic.com", c)
	}
	if c := chain[1]; c.Provider != ProviderOpenAI || c.Key != "sk-proj-openai-key" || c.Model != "gpt-5-mini" || c.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("second entry %+v, want ChatGPT on its own key at api.openai.com", c)
	}
	if c := chain[2]; !c.IsLocal() || c.Model != "docfind-fa" {
		t.Errorf("third entry %+v, want the local model", c)
	}
	for i, c := range chain[1:] {
		if strings.Contains(c.Key, "sk-ant") {
			t.Errorf("entry %d inherited the Anthropic key", i+2)
		}
		if strings.HasPrefix(c.Model, "claude") {
			t.Errorf("entry %d inherited a Claude model name", i+2)
		}
	}
}

// The old behaviour stays for the case it was written for: another model on
// the same account needs only its model name.
func TestSameProviderFallbackStillInherits(t *testing.T) {
	clearChainEnv(t)
	t.Setenv("DOCFIND_AI_PROVIDER", "openai")
	t.Setenv("DOCFIND_AI_KEY", "sk-proj-k")
	t.Setenv("DOCFIND_AI_MODEL", "gpt-5-mini")
	t.Setenv("DOCFIND_AI_MODEL_2", "gpt-5-nano")
	chain := ChainFromEnv()
	if len(chain) != 2 || chain[1].Key != "sk-proj-k" || chain[1].BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("second model on the same key lost its inheritance: %+v", chain)
	}
}

// Anthropic keys start with "sk-" too; they must not be taken for OpenAI.
func TestAnAnthropicKeyIsNotMistakenForOpenAI(t *testing.T) {
	clearChainEnv(t)
	t.Setenv("DOCFIND_AI_KEY", "sk-ant-api03-xyz")
	c := FromEnv()
	if c.Provider != ProviderAnthropic || c.BaseURL != "https://api.anthropic.com/v1" || c.Model != "claude-opus-5-5" {
		t.Fatalf("an sk-ant- key resolved to %+v", c)
	}
	if !c.Enabled() {
		t.Fatal("a keyed Anthropic entry should be enabled")
	}
	if (Config{Provider: ProviderAnthropic}).Enabled() {
		t.Fatal("an Anthropic entry without a key must stay off")
	}
}

func TestClaudeRefusalStopsTheChain(t *testing.T) {
	_, err := anthropicText([]byte(`{"content":[],"stop_reason":"refusal"}`))
	if err == nil {
		t.Fatal("a refusal was not reported")
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		t.Error("a refusal is a ProviderError, so the chain would retry it on every other model")
	}
}

func TestClaudeOutOfTokensSaysSo(t *testing.T) {
	_, err := anthropicText([]byte(`{"content":[],"stop_reason":"max_tokens","usage":{"output_tokens":4096}}`))
	if err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("empty answer at the token ceiling gave %v", err)
	}
}

// The first live whole-page answer stopped mid-sentence at the ceiling with no
// sign that it was incomplete. It must now say so.
func TestClaudeCutAtTheCeilingSaysItIsIncomplete(t *testing.T) {
	text, err := anthropicText([]byte(`{"content":[{"type":"text","text":"بند الف و ب"}],"stop_reason":"max_tokens"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "بند الف و ب") || !strings.HasSuffix(text, strings.TrimSpace(truncatedNote)) {
		t.Errorf("a truncated answer reads %q", text)
	}
}

func TestClaudeGetsTimeForALongAnswer(t *testing.T) {
	c := Config{Provider: ProviderAnthropic, Key: "k", BaseURL: "https://api.anthropic.com/v1"}
	t.Setenv("DOCFIND_AI_TIMEOUT", "")
	if got := c.Timeout(); got != anthropicTimeout || got >= 5*60*1e9 {
		t.Errorf("Claude timeout %v, want %v and under the server's 5-minute write timeout", got, anthropicTimeout)
	}
}

func TestCompleteTalksToTheMessagesAPI(t *testing.T) {
	var got struct {
		path, key, version, auth string
		body                     map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.key = r.Header.Get("x-api-key")
		got.version = r.Header.Get("anthropic-version")
		got.auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got.body)
		w.Write([]byte(`{"content":[{"type":"text","text":"تفسیر"},{"type":"text","text":" ادامه"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := Config{Provider: ProviderAnthropic, Key: "sk-ant-k", Model: "claude-opus-5-5", BaseURL: srv.URL}
	answer, err := Complete(context.Background(), c, "SYSTEM-TEXT", "f")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "تفسیر ادامه" {
		t.Errorf("answer %q, want the text blocks joined", answer)
	}
	if got.path != "/messages" || got.key != "sk-ant-k" || got.version != anthropicVersion {
		t.Errorf("request went to %q with key %q version %q", got.path, got.key, got.version)
	}
	if got.auth != "" {
		t.Errorf("an Authorization header was sent: Anthropic authenticates with x-api-key")
	}
	if s, _ := got.body["system"].(string); s != "SYSTEM-TEXT" {
		t.Errorf("the system message did not reach Claude's system field: %q", s)
	}
	if n, _ := got.body["max_tokens"].(float64); n < 4096 {
		t.Errorf("max_tokens %v: too small for Claude's Persian answer", n)
	}
	if _, ok := got.body["temperature"]; ok {
		t.Error("temperature sent: an unaccepted sampling parameter is a 400 that the chain will not fall past")
	}
}

// An exhausted Anthropic account must hand over to ChatGPT, exactly as an
// exhausted OpenAI one does.
func TestChainFallsFromClaudeToChatGPT(t *testing.T) {
	broke := failServer(t, http.StatusTooManyRequests,
		`{"type":"error","error":{"type":"rate_limit_error","message":"Your credit balance is too low"}}`)
	defer broke.Close()
	good := okServer(t, "پاسخ")
	defer good.Close()
	chain := []Config{
		{Provider: ProviderAnthropic, Key: "sk-ant-k", Model: "claude-opus-5-5", BaseURL: broke.URL},
		cfg(good.URL, "gpt-5-mini"),
	}
	answer, used, err := CompleteChain(context.Background(), chain, "", "q")
	if err != nil {
		t.Fatalf("chain stopped at Claude: %v", err)
	}
	if answer != "پاسخ" || used.Provider != ProviderOpenAI {
		t.Errorf("answer %q from %+v", answer, used)
	}
}

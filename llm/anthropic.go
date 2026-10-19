package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Anthropic's Messages API.
//
// WHY NOT THE OPENAI-COMPATIBLE ENDPOINT. Anthropic does answer the OpenAI
// chat-completions shape at api.anthropic.com/v1, which would have needed no
// code at all. It was not used, for two reasons that both show up as a broken
// feature rather than a worse answer. Anthropic documents that layer as a
// compatibility shim for testing, not the supported interface. And everything
// in this package that keys off "openai" would have quietly applied to it:
// EmbedConfig would pick it for embeddings (Anthropic has no embeddings
// endpoint), Describe would label Claude's answers "openai", and the credit
// checks' notion of "an OpenAI entry" would have to start reading URLs. A
// provider of its own costs one request builder and one reply parser.

const (
	// ProviderAnthropic is Claude, through Anthropic's Messages API.
	ProviderAnthropic = "anthropic"
	anthropicBaseURL  = "https://api.anthropic.com/v1"
	// anthropicVersion is the API version header the Messages API requires.
	anthropicVersion = "2023-06-01"
	// defaultAnthropicModel is the model used when none is named.
	defaultAnthropicModel = "claude-opus-5-5"
)

// anthropicTimeout: Claude writing a long Persian answer takes longer than the
// two minutes a hosted model is otherwise given. Keep it under the
// WriteTimeout of any server that waits on it (see localTimeout).
const anthropicTimeout = 4 * time.Minute

// truncatedNote is appended when Claude stops at the ceiling with text in hand,
// so the reader is told the answer is incomplete instead of finding out
// mid-sentence.
const truncatedNote = "\n\n(این پاسخ به سقف طول رسید و ممکن است ناتمام باشد.)"

// anthropicTokens is the output ceiling for one reply.
//
// Claude spends noticeably more tokens than the OpenAI models on the same
// Persian text, and a long answer runs to four or five thousand
// characters -- so the 900/1,400 ceilings tuned for gpt-4.1-mini would cut
// Claude's answer off mid-sentence. A ceiling is billed only for what is
// written, so raising it costs nothing on a short answer. Small budgets (the
// health probe's 8, a router's few hundred) are left as asked:
// there the small number is the point.
func anthropicTokens(maxTokens int) int {
	if maxTokens >= 900 && maxTokens < 4096 {
		return 4096
	}
	return maxTokens
}

// anthropicRequestWith builds one Messages API request.
//
// No temperature is sent. The OpenAI path sends 0.4 so two readers see similar
// answers, but an unaccepted sampling parameter is a 400 on every request, and
// 400 is deliberately not a reason to fall back to the next model -- so a
// wrong guess here would take the whole feature down instead of degrading to
// ChatGPT. The API default is fine.
func anthropicRequestWith(ctx context.Context, cfg Config, system, prompt string, maxTokens int) (*http.Request, error) {
	payload := map[string]any{
		"model":      cfg.Model,
		"max_tokens": anthropicTokens(maxTokens),
		"messages": []any{
			map[string]string{"role": "user", "content": prompt},
		},
	}
	if s := strings.TrimSpace(system); s != "" {
		payload["system"] = s
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	base := cfg.BaseURL
	if base == "" {
		base = anthropicBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.Key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("User-Agent", "docfind-ai/1.0")
	return req, nil
}

// anthropicText reads a Messages API reply.
//
// A refusal is returned as a plain error, not a *ProviderError, for the same
// reason Gemini's blockReason is: the next model would refuse the same prompt,
// and walking the whole chain to prove it spends every quota on nothing.
func anthropicText(body []byte) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("پاسخ سرویس هوش مصنوعی قابل خواندن نبود: %w", err)
	}
	if out.StopReason == "refusal" {
		return "", errors.New("سرویس هوش مصنوعی درخواست را رد کرد: refusal")
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	text := b.String()
	if out.StopReason == "max_tokens" {
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("مدل به سقف توکن رسید و متنی باقی نماند (%d توکن). سقف max_tokens باید بالاتر برود",
				out.Usage.OutputTokens)
		}
		text = strings.TrimRight(text, " \n") + truncatedNote
	}
	return text, nil
}

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// A prompt with a budget: Complete with the output ceiling as a parameter, for
// a caller that wants fifty tokens of JSON or a page of prose and knows which.
// The same pipeline -- the HTTP/1.1 client, the request shapes that differ
// between the gpt-5 generation and everything older, provider errors
// unwrapped into a sentence somebody can act on, and the fallback chain.

// Prompt is a question sent as written.
type Prompt struct {
	// System is the instruction the model is given as its role. It reaches an
	// OpenAI-shaped provider as the system message; Gemini takes a single
	// content, so it is prepended to the user text with a blank line between.
	System string
	User   string
	// MaxTokens bounds the answer for the models that take max_tokens.
	//
	// A reasoning model ignores it and uses reasoningBudget instead, for the
	// reason recorded at that constant: its budget covers hidden reasoning as
	// well as the answer, and sizing it to the answer produces an empty string
	// with finish_reason "length" and no error at all. A fifty-token JSON
	// object would hit that immediately.
	MaxTokens int
}

// AskPrompt sends one prompt to one model.
func AskPrompt(ctx context.Context, cfg Config, p Prompt) (string, error) {
	if !cfg.Enabled() {
		return "", errors.New("هوش مصنوعی پیکربندی نشده است")
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = 900
	}
	c, cancel := context.WithTimeout(ctx, cfg.Timeout())
	defer cancel()

	var (
		req *http.Request
		err error
	)
	switch cfg.Provider {
	case ProviderGemini:
		text := p.User
		if p.System != "" {
			text = p.System + "\n\n" + p.User
		}
		req, err = geminiRequestWith(c, cfg, text, p.MaxTokens)
	case ProviderOpenAI:
		req, err = openAIRequestWith(c, cfg, p.System, p.User, p.MaxTokens)
	case ProviderAnthropic:
		req, err = anthropicRequestWith(c, cfg, p.System, p.User, p.MaxTokens)
	default:
		return "", fmt.Errorf("سرویس هوش مصنوعی ناشناخته است: %s", cfg.Provider)
	}
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", &ProviderError{
			Transport: true, Local: cfg.IsLocal(),
			Provider: cfg.Provider, Model: cfg.Model,
			Msg: scrub(err, cfg.Key).Error(),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, replyLimit))
	if err != nil {
		return "", &ProviderError{
			Transport: true, Local: cfg.IsLocal(),
			Provider: cfg.Provider, Model: cfg.Model,
			Msg: scrub(err, cfg.Key).Error(),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &ProviderError{
			Status: resp.StatusCode, Provider: cfg.Provider, Model: cfg.Model,
			Msg: redactKey(providerError(body), cfg.Key),
		}
	}

	text, err := replyText(cfg, body)
	if err != nil {
		return "", err
	}
	if text = strings.TrimSpace(text); text == "" {
		// Typed, so the chain treats it as worth trying elsewhere: a model that
		// answers with nothing is as useless as one that refuses.
		return "", &ProviderError{Empty: true, Provider: cfg.Provider, Model: cfg.Model}
	}
	return text, nil
}

// AskPromptChain tries each configured model until one answers, and reports
// which one did: a key can be perfectly valid and still refuse every request.
func AskPromptChain(ctx context.Context, chain []Config, p Prompt) (string, Config, error) {
	return tryChain(ctx, chain, func(ctx context.Context, cfg Config) (string, error) {
		return AskPrompt(ctx, cfg, p)
	})
}

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// One prompt, one answer, no doctrine attached.
//
// The caller's system message goes out verbatim and the answer comes back as
// the model wrote it. What is shared with the rest of the package is the
// TRANSPORT: the request shapes, the reply parsers, the key redaction and the
// typed ProviderError, which is the part that was expensive to get right --
// how the output budget is named per model family, which models refuse a
// temperature, how each provider reports an error without leaking the key.

// CompleteChain tries each configured model until one answers.
//
// The same fallback policy every chain call uses, because the questions differ
// but "which failures are worth trying elsewhere" does not: an exhausted key,
// a transport failure and an empty answer are all worth the next entry, and a
// prompt refused on content grounds is not.
func CompleteChain(ctx context.Context, chain []Config, system, prompt string) (string, Config, error) {
	return tryChain(ctx, chain, func(ctx context.Context, cfg Config) (string, error) {
		return Complete(ctx, cfg, system, prompt)
	})
}

// completeTokens bounds a reply.
//
// Sized for structured tasks -- a list in, a list out -- rather than long
// prose. A reasoning model spends
// its hidden reasoning from the same budget, so this is not "how long may the
// answer be" but "how much may it think and answer"; too small and a reasoning
// model returns nothing at all, having spent the lot on thinking.
const completeTokens = 3000

// Complete sends one system message and one prompt, and returns the reply.
//
// The system message is the caller's, verbatim. Nothing is added to it and
// nothing is stripped from the answer: a caller that needs a particular shape
// of reply asks for it and validates what comes back, because this function
// has no idea what shape would be right.
//
// Errors are the same typed ones Ask returns -- *ProviderError for a transport
// failure, an HTTP status, or an empty answer -- so a caller can tell "try
// somewhere else" from "this was refused".
func Complete(ctx context.Context, cfg Config, system, prompt string) (string, error) {
	if !cfg.Enabled() {
		return "", errors.New("هوش مصنوعی پیکربندی نشده است")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("llm: nothing to ask")
	}

	c, cancel := context.WithTimeout(ctx, cfg.Timeout())
	defer cancel()

	var (
		req *http.Request
		err error
	)
	switch cfg.Provider {
	case ProviderGemini:
		// Gemini takes a single content with no separate system field, so the
		// instruction rides at the top of the prompt -- the same thing Ask
		// does for the same reason.
		text := prompt
		if s := strings.TrimSpace(system); s != "" {
			text = s + "\n\n" + prompt
		}
		req, err = geminiRequestWith(c, cfg, text, completeTokens)
	case ProviderOpenAI:
		req, err = openAIRequestWith(c, cfg, system, prompt, completeTokens)
	case ProviderAnthropic:
		req, err = anthropicRequestWith(c, cfg, system, prompt, completeTokens)
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
	text = strings.TrimSpace(text)
	if text == "" {
		// Typed, so a chain treats it as worth trying elsewhere: a model that
		// answers with nothing is as useless as one that refuses.
		return "", &ProviderError{Empty: true, Provider: cfg.Provider, Model: cfg.Model}
	}
	return text, nil
}

// Model asks a chain through one method, Complete(ctx, system, user),
// returning the text and which provider answered -- the shape
// agent.LanguageModel takes, so the agent's planner gets this package's
// fallback order and circuit breakers without either package importing the
// other.
type Model struct{ Chain []Config }

// Complete sends one system message and one prompt through the chain.
func (m Model) Complete(ctx context.Context, system, user string) (string, string, error) {
	text, cfg, err := CompleteChain(ctx, m.Chain, system, user)
	if err != nil {
		return "", "", err
	}
	return text, cfg.Describe(), nil
}

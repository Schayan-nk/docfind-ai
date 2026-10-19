package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/Schayan-nk/docfind-ai/dependencies"
)

// Circuit breakers for the chain's providers.
//
// Without them a provider that is down costs every question its whole
// timeout before the chain moves on: OpenAI unreachable from the office meant
// a minute of nothing before the local model was even asked. With them, two
// failures in a row set the provider aside for thirty seconds, then longer
// each time it is tried and still fails, and the chain goes straight to the
// next one; the first question after the cooldown finds out whether it is
// back.
//
// One breaker per ADDRESS, not per model: what fails is the endpoint, and
// every model behind it with it.

var breakers atomic.Pointer[dependencies.Registry]

// UseBreakers hands the chain the dependency registry its breakers live in.
// Without it the chain asks every provider every time, as it always did.
func UseBreakers(r *dependencies.Registry) { breakers.Store(r) }

func breakerName(c Config) string {
	host := c.Provider
	if u, err := url.Parse(c.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return "ai:" + c.Provider + "@" + host
}

func ensureBreaker(r *dependencies.Registry, c Config) string {
	name := breakerName(c)
	r.Ensure(dependencies.Spec{
		Name:  name,
		Label: "هوش مصنوعی: " + c.Describe(),
		// A backstop only: the request carries its own deadline.
		MaxWait:       10 * time.Minute,
		SlowAfter:     90 * time.Second,
		FailThreshold: 2,
		Cooldown:      30 * time.Second,
		MaxCooldown:   10 * time.Minute,
		Fallback:      "مدل بعدی در زنجیره، و در پایان قواعد ثابت بدون هوش مصنوعی.",
		Downgrade:     []string{"ai"},
		// Its own check, so a provider that failed is found back without
		// waiting for somebody to ask it something: the status bar said
		// "در دسترس نیست" for hours after the account was recharged.
		// On the breaker's schedule while it is set aside;
		// every six hours while it answers, to notice credit running out.
		Check:    probe(c),
		Interval: 6 * time.Hour,
	})
	return name
}

// probe is a breaker's check: the smallest request the provider takes,
// judged by the status of its answer alone. 200 is a key it accepts with
// credit behind it; what the model wrote does not matter (a reasoning model
// may write nothing at all for "ok"). A refusal that says nothing about the
// provider -- the prompt, not the endpoint -- passes, as it does for a real
// question.
func probe(cfg Config) func(context.Context) error {
	return func(ctx context.Context) error {
		c, cancel := context.WithTimeout(ctx, min(cfg.Timeout(), 2*time.Minute))
		defer cancel()
		var (
			req *http.Request
			err error
		)
		switch cfg.Provider {
		case ProviderGemini:
			req, err = geminiRequestWith(c, cfg, "Reply with the word ok.", 8)
		case ProviderOpenAI:
			req, err = openAIRequestWith(c, cfg, "Reply with the word ok.", "ok?", 8)
		case ProviderAnthropic:
			req, err = anthropicRequestWith(c, cfg, "Reply with the word ok.", "ok?", 8)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return &ProviderError{Transport: true, Local: cfg.IsLocal(), Provider: cfg.Provider, Model: cfg.Model, Msg: scrub(err, cfg.Key).Error()}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, replyLimit))
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		pe := &ProviderError{Status: resp.StatusCode, Provider: cfg.Provider, Model: cfg.Model, Msg: redactKey(providerError(body), cfg.Key)}
		if !pe.Retryable() {
			return nil
		}
		return pe
	}
}

// Available reports whether any provider in the chain would be asked now --
// false while every one of them is set aside, which is when the callers turn
// to their rules.
func Available(chain []Config) bool {
	if len(chain) == 0 {
		return false
	}
	r := breakers.Load()
	if r == nil {
		return true
	}
	for _, c := range chain {
		if r.Allow(breakerName(c)) {
			return true
		}
	}
	return false
}

// errSetAside marks a provider skipped because its breaker is open.
var errSetAside = errors.New("موقتاً کنار گذاشته شده؛ به‌تازگی پاسخ نداده است")

// askGuarded asks one provider through its breaker. Only a failure that says
// something about the provider counts against it -- a timeout, a refused
// connection, a 5xx, a rate limit; a prompt the model declined, or a caller
// who gave up, does not.
func askGuarded(ctx context.Context, cfg Config, ask func(context.Context, Config) (string, error)) (string, error) {
	r := breakers.Load()
	if r == nil {
		return ask(ctx, cfg)
	}
	var answer string
	var callErr error
	err := r.Do(ctx, ensureBreaker(r, cfg), func(ctx context.Context) error {
		answer, callErr = ask(ctx, cfg)
		var pe *ProviderError
		if callErr != nil && errors.As(callErr, &pe) && !pe.Retryable() {
			return nil // the provider answered; the answer was a refusal
		}
		return callErr
	})
	if errors.Is(err, dependencies.ErrOpen) {
		return "", errSetAside
	}
	if callErr != nil {
		return "", callErr
	}
	return answer, err
}

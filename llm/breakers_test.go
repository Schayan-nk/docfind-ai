package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Schayan-nk/docfind-ai/dependencies"
)

// A provider that keeps failing is set aside: after two failures the chain
// goes straight to the next one without asking it, and says so.
func TestAFailingProviderIsSetAside(t *testing.T) {
	reg := dependencies.New()
	UseBreakers(reg)
	t.Cleanup(func() { UseBreakers(nil) })

	cloud := Config{Provider: "openai", Model: "gpt", BaseURL: "https://api.openai.com/v1", Key: "k"}
	local := Config{Provider: "openai", Model: "qwen", BaseURL: "http://127.0.0.1:11434/v1"}
	calls := map[string]int{}
	ask := func(_ context.Context, c Config) (string, error) {
		calls[c.Model]++
		if c.Model == "gpt" {
			return "", &ProviderError{Transport: true, Provider: c.Provider, Model: c.Model}
		}
		return "پاسخ محلی", nil
	}
	for range 4 {
		answer, used, err := tryChain(context.Background(), []Config{cloud, local}, ask)
		if err != nil || used.Model != "qwen" || answer == "" {
			t.Fatalf("answer %q from %q: %v", answer, used.Model, err)
		}
	}
	if calls["gpt"] != 2 || calls["qwen"] != 4 {
		t.Errorf("calls = %v; the failing provider should have been asked twice, then set aside", calls)
	}
	if !Available([]Config{cloud, local}) || Available([]Config{cloud}) {
		t.Error("Available should be true while one provider answers, false when the only one is set aside")
	}
}

// A prompt the model refused is an answer, not an outage: it does not count
// against the provider.
func TestARefusalIsNotAnOutage(t *testing.T) {
	reg := dependencies.New()
	UseBreakers(reg)
	t.Cleanup(func() { UseBreakers(nil) })
	c := Config{Provider: "openai", Model: "gpt", BaseURL: "https://api.openai.com/v1", Key: "k"}
	refuse := func(context.Context, Config) (string, error) {
		return "", &ProviderError{Status: 400, Msg: "content policy"}
	}
	for range 5 {
		_, _, err := tryChain(context.Background(), []Config{c}, refuse)
		var pe *ProviderError
		if !errors.As(err, &pe) || pe.Status != 400 {
			t.Fatalf("err = %v", err)
		}
	}
	if st, _ := reg.Status(breakerName(c)); st.Breaker != dependencies.Closed || st.Failures != 0 {
		t.Errorf("breaker = %+v", st)
	}
}

// A provider is found back by its breaker's own check, without anybody
// asking it a question: the status bar named OpenAI "not available" for
// hours after the account was recharged (2026-10-06), because nothing but a
// real question ever tried it again.
func TestASetAsideProviderIsFoundBackByItsOwnCheck(t *testing.T) {
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota"}}`))
	}))
	defer srv.Close()
	cfg := Config{Provider: ProviderOpenAI, Model: "gpt-5-mini", BaseURL: srv.URL, Key: "sk-test"}

	check := probe(cfg)
	if err := check(context.Background()); err == nil {
		t.Fatal("out of quota passed the check")
	}
	status = http.StatusUnauthorized
	if err := check(context.Background()); err == nil {
		t.Fatal("a refused key passed the check")
	}
	status = http.StatusBadRequest
	if err := check(context.Background()); err != nil {
		t.Errorf("a refusal of the request, not of the provider, failed the check: %v", err)
	}
	status = http.StatusOK
	if err := check(context.Background()); err != nil {
		t.Errorf("an answer failed the check: %v", err)
	}

	// Through the registry: "check again" (CheckAll) runs only the
	// dependencies that have a check of their own, so the breaker opening
	// from it alone proves the AI provider now has one -- and the registry
	// closes a breaker whose check passes (dependencies' own tests).
	reg := dependencies.New()
	UseBreakers(reg)
	t.Cleanup(func() { UseBreakers(nil) })
	status = http.StatusTooManyRequests
	name := ensureBreaker(reg, cfg)
	reg.CheckAll(context.Background())
	reg.CheckAll(context.Background())
	if st, _ := reg.Status(name); st.Breaker == dependencies.Closed || st.Failures != 2 {
		t.Fatalf("two failed checks: %+v", st)
	}
}

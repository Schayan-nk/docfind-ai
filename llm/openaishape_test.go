package llm

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

// The default model has to be one the configured endpoint recognises.
//
// "openai" in this package means the OpenAI-compatible request shape, not
// OpenAI the company: the same code talks to Groq, to a local relay, and to
// api.openai.com. The default was "llama-3.3-70b-versatile" for all of them --
// a Groq model name -- so the first time a real OpenAI key was configured
// without naming a model, every request would have come back "model not found".
// Nothing in the application would have said why.
func TestTheDefaultModelFollowsTheEndpoint(t *testing.T) {
	cases := []struct {
		url, want, why string
	}{
		{"https://api.openai.com/v1", "gpt-5-mini",
			"a real OpenAI endpoint needs a real OpenAI model name"},
		{"https://api.groq.com/openai/v1", "llama-3.3-70b-versatile",
			"Groq serves llama; an OpenAI model name would not resolve"},
		{"http://127.0.0.1:11434/v1", "",
			"a local relay serves whatever it was configured with, and guessing a " +
				"hosted model name would be the reason a request failed"},
	}
	for _, c := range cases {
		if got := defaultOpenAIModel(c.url); got != c.want {
			t.Errorf("defaultOpenAIModel(%q) = %q, want %q -- %s", c.url, got, c.want, c.why)
		}
	}

	// And through the real config path, which is how it actually reaches a
	// request: provider set, key set, no model named.
	t.Setenv("DOCFIND_AI_PROVIDER", "openai")
	t.Setenv("DOCFIND_AI_KEY", "sk-not-a-real-key")
	t.Setenv("DOCFIND_AI_URL", "https://api.openai.com/v1")
	t.Setenv("DOCFIND_AI_MODEL", "")
	if got := FromEnv().Model; got != "gpt-5-mini" {
		t.Errorf("a configured OpenAI account with no model named resolves to %q, want "+
			"gpt-5-mini", got)
	}
}

// The request shape has to match the model family, or nothing works at all.
//
// The gpt-5 generation and the o-series renamed max_tokens to
// max_completion_tokens and reject the old spelling; several also refuse any
// temperature but the default. Older chat models want max_tokens and accept a
// temperature. Sending the wrong pair is not a worse answer, it is a 400 on
// every request -- so this checks the bytes that actually go on the wire.
func TestTheRequestShapeMatchesTheModelFamily(t *testing.T) {
	bodyFor := func(model string) map[string]any {
		cfg := Config{Provider: ProviderOpenAI, Key: "k",
			BaseURL: "https://api.openai.com/v1", Model: model}
		req, err := openAIRequestWith(context.Background(), cfg, "SYSTEM", "PROMPT", 1400)
		if err != nil {
			t.Fatalf("building the request for %q: %v", model, err)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("reading the body for %q: %v", model, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("the request body for %q is not JSON: %v", model, err)
		}
		return m
	}

	// Reasoning families: the new name, no temperature, a budget big enough to
	// survive the thinking, and the effort turned down.
	for _, model := range []string{"gpt-5-mini", "gpt-5-nano", "gpt-5", "o3", "o4-mini"} {
		b := bodyFor(model)
		if _, ok := b["max_completion_tokens"]; !ok {
			t.Errorf("%s: max_completion_tokens is missing; this family rejects max_tokens "+
				"and every request would fail", model)
		}
		if _, ok := b["max_tokens"]; ok {
			t.Errorf("%s: max_tokens was sent; this family rejects it outright", model)
		}
		if _, ok := b["temperature"]; ok {
			t.Errorf("%s: a temperature was sent; several models in this family refuse "+
				"anything but the default", model)
		}

		// The budget covers thinking AND answer, and the model is billed for
		// both. At 1,400 -- a budget tuned for older models -- gpt-5-mini spent
		// the entire budget reasoning about a five-thousand-token prompt and
		// returned an empty string with finish_reason "length", on every
		// single request, silently. Measured, not guessed.
		if got, _ := b["max_completion_tokens"].(float64); got < 4000 {
			t.Errorf("%s: max_completion_tokens is %v. A reasoning model pays for its "+
				"thinking out of this budget; below about 4,000 a long prompt "+
				"returns an empty answer and nothing says why", model, b["max_completion_tokens"])
		}
		// Low effort measured fastest, cheapest AND longest-answered on a long
		// question. Dropping it is not a neutral change.
		if b["reasoning_effort"] != "low" {
			t.Errorf("%s: reasoning_effort is %v, want \"low\" -- at default effort the "+
				"same question took 29-34s instead of 20s and returned a SHORTER answer",
				model, b["reasoning_effort"])
		}
	}

	// Older chat models, and every non-OpenAI compatible endpoint: the old name
	// and a temperature.
	for _, model := range []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1-mini", "llama-3.3-70b-versatile"} {
		b := bodyFor(model)
		if _, ok := b["max_tokens"]; !ok {
			t.Errorf("%s: max_tokens is missing; this model does not know "+
				"max_completion_tokens", model)
		}
		if _, ok := b["max_completion_tokens"]; ok {
			t.Errorf("%s: max_completion_tokens was sent to a model that predates it", model)
		}
		if _, ok := b["temperature"]; !ok {
			t.Errorf("%s: no temperature was sent, so answers drift more than they need to",
				model)
		}
		if _, ok := b["reasoning_effort"]; ok {
			t.Errorf("%s: reasoning_effort was sent to a model that has no reasoning "+
				"phase; endpoints that predate the parameter reject the whole request",
				model)
		}
	}

	// An unknown model falls through to the older shape, which is what every
	// compatible third-party endpoint expects. Guessing the new shape for
	// something unrecognised would break the relays that work today.
	if b := bodyFor("some-future-local-model"); b["max_tokens"] == nil {
		t.Error("an unrecognised model did not fall back to the widely-supported shape")
	}
}

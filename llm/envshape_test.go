package llm

import "testing"

// What a plausible .env actually resolves to.
//
// Every case here is a file somebody would reasonably write, and two of them
// used to fail in the worst possible way: not with an error, but by quietly
// doing something else. A key meant for OpenAI went to Google because Gemini
// was the historical default, and a provider named without an address turned
// the whole feature off -- in both cases the page looked normal and the OpenAI
// account showed no usage, which reads as "the button does nothing".
func TestWhatAPlausibleEnvFileResolvesTo(t *testing.T) {
	cases := []struct {
		name                             string
		provider, key, url, model        string
		wantProvider, wantURL, wantModel string
		wantEnabled                      bool
		why                              string
	}{
		{
			name:         "key only, no provider named",
			key:          "sk-proj-abc123",
			wantProvider: ProviderOpenAI, wantURL: "https://api.openai.com/v1",
			wantModel: "gpt-5-mini", wantEnabled: true,
			why: "an sk- key is an OpenAI key; sending it to Gemini spends nothing and " +
				"explains nothing",
		},
		{
			name:     "provider and key, no URL",
			provider: "openai", key: "sk-proj-abc123",
			wantProvider: ProviderOpenAI, wantURL: "https://api.openai.com/v1",
			wantModel: "gpt-5-mini", wantEnabled: true,
			why: "\"openai\" with a key cannot mean any endpoint but OpenAI's; this used " +
				"to disable the feature and remove the card from the page entirely",
		},
		{
			name:     "all three set, the documented form",
			provider: "openai", key: "sk-proj-abc123", url: "https://api.openai.com/v1",
			wantProvider: ProviderOpenAI, wantURL: "https://api.openai.com/v1",
			wantModel: "gpt-5-mini", wantEnabled: true,
			why: "the form the deploy notes give must keep working unchanged",
		},
		{
			name:     "a trailing slash on the URL",
			provider: "openai", key: "sk-proj-abc123", url: "https://api.openai.com/v1/",
			wantProvider: ProviderOpenAI, wantURL: "https://api.openai.com/v1",
			wantModel: "gpt-5-mini", wantEnabled: true,
			why: "a copied URL usually carries one, and doubling the slash 404s every call",
		},
		{
			name:     "an explicit model still wins",
			provider: "openai", key: "sk-proj-abc123", url: "https://api.openai.com/v1",
			model:        "gpt-4o-mini",
			wantProvider: ProviderOpenAI, wantURL: "https://api.openai.com/v1",
			wantModel: "gpt-4o-mini", wantEnabled: true,
			why: "the override is how a different model gets chosen without a rebuild",
		},
		{
			name:     "a local relay keeps working with no key",
			provider: "openai", url: "http://127.0.0.1:11434/v1",
			wantProvider: ProviderOpenAI, wantURL: "http://127.0.0.1:11434/v1",
			wantModel: "", wantEnabled: true,
			why: "a relay holds its own credentials; demanding a key would mean inventing " +
				"a fake one, and guessing a hosted model name for it would break the call",
		},
		{
			name:     "a real Gemini key still reaches Gemini",
			provider: "gemini", key: "AIzaSyNotARealKey",
			wantProvider: ProviderGemini, wantURL: "",
			wantModel: "gemini-2.0-flash", wantEnabled: true,
			why: "the sk- inference must not disturb the provider it used to default to",
		},
		{
			name:         "nothing configured at all",
			wantProvider: ProviderGemini, wantURL: "", wantModel: "gemini-2.0-flash",
			wantEnabled: false,
			why:         "off by default: nothing leaves the building unless it was set up",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DOCFIND_AI_PROVIDER", c.provider)
			t.Setenv("DOCFIND_AI_KEY", c.key)
			t.Setenv("DOCFIND_AI_URL", c.url)
			t.Setenv("DOCFIND_AI_MODEL", c.model)

			got := FromEnv()
			if got.Provider != c.wantProvider {
				t.Errorf("provider = %q, want %q -- %s", got.Provider, c.wantProvider, c.why)
			}
			if got.BaseURL != c.wantURL {
				t.Errorf("base URL = %q, want %q -- %s", got.BaseURL, c.wantURL, c.why)
			}
			if got.Model != c.wantModel {
				t.Errorf("model = %q, want %q -- %s", got.Model, c.wantModel, c.why)
			}
			if got.Enabled() != c.wantEnabled {
				t.Errorf("enabled = %v, want %v -- %s", got.Enabled(), c.wantEnabled, c.why)
			}
			// And through the chain, which is what the server actually holds:
			// a config that resolves correctly but produces an empty chain
			// still leaves the page with no card on it.
			if ChainEnabled(ChainFromEnv()) != c.wantEnabled {
				t.Errorf("the chain reports enabled = %v, want %v -- the card is rendered "+
					"off this, not off FromEnv", ChainEnabled(ChainFromEnv()), c.wantEnabled)
			}
		})
	}
}

// An OpenAI key must never be addressed to Gemini.
//
// Stated on its own because it is the specific mistake that happened, and a
// table entry is easy to delete without noticing what it protected. Sending a
// key to the wrong provider is worse than sending none: it fails at the far
// end, where nothing in this application can see or report it.
func TestAnOpenAIKeyIsNeverSentToGemini(t *testing.T) {
	for _, key := range []string{"sk-proj-abc", "sk-abc123", "sk-None-Of-This-Is-Real"} {
		t.Setenv("DOCFIND_AI_PROVIDER", "")
		t.Setenv("DOCFIND_AI_KEY", key)
		t.Setenv("DOCFIND_AI_URL", "")
		t.Setenv("DOCFIND_AI_MODEL", "")
		if got := FromEnv(); got.Provider == ProviderGemini {
			t.Errorf("key %q with no provider named resolved to Gemini; it would be sent "+
				"to Google, rejected there, and the OpenAI account would show no usage "+
				"at all", key)
		}
	}
}

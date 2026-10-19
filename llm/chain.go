package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// A fallback chain: several models tried in order until one answers.
//
// The reason this exists is the failure that prompted it. A key can be
// perfectly valid and still refuse every request -- the first key configured
// here returned HTTP 429 `credit_balance_exhausted` on every model in the
// account, because the account had no credit. No amount of retrying that
// provider would have helped, and the person asking just saw a
// failure. With a chain, a second provider answers instead.
//
// The distinction the chain rests on is in ProviderError.Retryable: a refused
// key, an exhausted quota, an unknown model or a provider that is down are all
// reasons to try the next entry, while a prompt the provider rejected on
// content grounds is not -- that would fail identically everywhere and would
// spend every quota in the list finding out.

// maxChain bounds how many entries are read from the environment. Five is
// more than anybody needs and stops a typo in a variable name turning into an
// unbounded scan.
const maxChain = 5

// ProviderError is a failure from one model, with enough detail for the chain
// to decide whether a different one could do better.
type ProviderError struct {
	// Status is the HTTP status, or zero when the request never got one.
	Status int
	Msg    string
	// Transport is set when the request did not complete at all -- a blocked
	// address, a dead host, a timeout.
	Transport bool
	// Empty is set when the call succeeded but the model returned nothing.
	Empty bool
	// Local is set when the model runs on this machine, which changes what a
	// transport failure means and therefore what the reader should be told.
	Local    bool
	Provider string
	Model    string
}

func (e *ProviderError) Error() string {
	switch {
	case e.Transport && e.Local:
		// Naming the web filter here would be actively misleading: Go never
		// sends a loopback request through a proxy, so the office filter
		// cannot touch it. On this machine the cause is the local service
		// being down, or the model still loading when the deadline hit.
		return "مدل روی همین سرور پاسخ نداد (سرویس اجرا نشده یا مدل هنوز در حال بارگذاری است)"
	case e.Transport:
		return "ارتباط با سرویس هوش مصنوعی برقرار نشد " +
			"(احتمالاً فیلتر شبکه یا نبود دسترسی اینترنت)"
	case e.Empty:
		return "سرویس هوش مصنوعی پاسخ خالی داد"
	default:
		// A provider's own message is kept: "you have no credits remaining" is
		// the one sentence that tells the reader what to do about it.
		return fmt.Sprintf("سرویس هوش مصنوعی خطا داد (HTTP %d): %s", e.Status, e.Msg)
	}
}

// Verbose is Error plus the underlying transport detail, for the server log.
//
// The raw Go error is kept out of Error() on purpose: «dial tcp
// 127.0.0.1:11434: connectex: No connection could be made because the target
// machine actively refused it» on a reader's screen is noise they cannot act
// on, and the sentence before it already says what is wrong. An administrator
// reading the log still gets the detail.
func (e *ProviderError) Verbose() string {
	if (e.Transport || e.Empty) && e.Msg != "" {
		return e.Error() + ": " + e.Msg
	}
	return e.Error()
}

// Retryable reports whether a different model might succeed where this one
// did not.
func (e *ProviderError) Retryable() bool {
	if e.Transport || e.Empty {
		return true
	}
	switch e.Status {
	case 401, 402, 403, 404, 408, 409, 413, 429:
		// Refused key, payment required, forbidden, unknown model, timeout,
		// conflict, prompt too large for this model, or out of quota.
		return true
	}
	return e.Status >= 500
}

// ChainFromEnv reads the primary configuration plus any numbered fallbacks.
//
//	DOCFIND_AI_PROVIDER / _URL / _MODEL / _KEY        the first choice
//	DOCFIND_AI_PROVIDER_2 / _URL_2 / _MODEL_2 / _KEY_2   the second, and so on
//
// A numbered entry inherits any field it does not set from the primary, so a
// second model on the same key needs only DOCFIND_AI_MODEL_2. DOCFIND_AI_MODEL
// also accepts a comma-separated list, which expands into one entry per model
// -- the cheapest way to say "this key, these models, in this order".
func ChainFromEnv() []Config {
	var out []Config
	primary := FromEnv()
	out = append(out, expandModels(primary)...)

	for n := 2; n <= maxChain; n++ {
		sfx := fmt.Sprintf("_%d", n)
		c := Config{
			Provider: env("DOCFIND_AI_PROVIDER" + sfx),
			Key:      env("DOCFIND_AI_KEY" + sfx),
			Model:    env("DOCFIND_AI_MODEL" + sfx),
			BaseURL:  strings.TrimRight(env("DOCFIND_AI_URL"+sfx), "/"),
		}
		if c == (Config{}) {
			continue // nothing configured at this position
		}
		// Inherited rather than required: the common case is another model on
		// the same account, and repeating the key four times invites one of
		// the copies to drift.
		if c.Provider == "" {
			c.Provider = primary.Provider
		}
		// ...but only from a primary of the SAME provider. With Claude first
		// and ChatGPT second, blind inheritance would send the Anthropic key to
		// api.openai.com and to the local model, and ask OpenAI for a model
		// called claude-opus-5-5 -- one provider's credential leaking to another
		// on every fallback. A different provider brings its own fields, and
		// normalize gives it that provider's defaults for anything left blank.
		if c.Provider == primary.Provider {
			if c.Key == "" {
				c.Key = primary.Key
			}
			if c.BaseURL == "" {
				c.BaseURL = primary.BaseURL
			}
			if c.Model == "" {
				c.Model = primary.Model
			}
		}
		out = append(out, expandModels(normalize(c))...)
	}

	// Only usable entries, and no duplicates: a chain that tries the same
	// dead model twice wastes a reader's time twice.
	seen := map[Config]bool{}
	chain := make([]Config, 0, len(out))
	for _, c := range out {
		if !c.Enabled() || seen[c] {
			continue
		}
		seen[c] = true
		chain = append(chain, c)
	}
	return chain
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

// expandModels turns one entry with "a,b,c" as its model into three entries.
func expandModels(c Config) []Config {
	if !strings.Contains(c.Model, ",") {
		return []Config{c}
	}
	var out []Config
	for _, m := range strings.Split(c.Model, ",") {
		if m = strings.TrimSpace(m); m != "" {
			cp := c
			cp.Model = m
			out = append(out, cp)
		}
	}
	return out
}

// ChainEnabled reports whether anything is configured to ask.
func ChainEnabled(chain []Config) bool { return len(chain) > 0 }

// DescribeChain names every entry in the order they will be tried.
//
// Naming only the first one was wrong: it told the reader their
// findings go to gpt-4.1-mini while the answer on screen had in fact come
// from the local fallback. Since this string sits next to a claim about where
// the data goes, it has to describe what can actually happen.
func DescribeChain(chain []Config) string {
	if len(chain) == 0 {
		return ""
	}
	names := make([]string, 0, len(chain))
	for _, c := range chain {
		names = append(names, c.Describe())
	}
	return strings.Join(names, "، سپس ")
}

// AllLocal reports whether every entry runs on this machine, which is the only
// case where it is true to say no data leaves the building.
func AllLocal(chain []Config) bool {
	if len(chain) == 0 {
		return false
	}
	for _, c := range chain {
		if !c.IsLocal() {
			return false
		}
	}
	return true
}

// tryChain is the fallback loop itself, with the question as a parameter.
//
// Extracted when a second kind of question needed the same behaviour. The loop is not the obvious part of this file -- which
// failures are worth trying elsewhere, why every attempt is kept rather than
// only the last, why a first-entry failure is returned as itself -- and a
// second copy of it would have drifted from this one on the first fix.
func tryChain(ctx context.Context, chain []Config,
	ask func(context.Context, Config) (string, error)) (string, Config, error) {
	if len(chain) == 0 {
		return "", Config{}, fmt.Errorf("هوش مصنوعی پیکربندی نشده است")
	}

	// Two transcripts: one the reader sees, one the log gets. They differ
	// because the useful-to-a-reader and useful-to-an-administrator halves of
	// a transport failure are not the same text.
	var tried, triedVerbose []string
	for i, cfg := range chain {
		// Through the provider's circuit breaker (breakers.go): one that has
		// just failed is set aside without being asked.
		answer, err := askGuarded(ctx, cfg, ask)
		if err == nil {
			return answer, cfg, nil
		}
		tried = append(tried, fmt.Sprintf("%s: %s", cfg.Describe(), err.Error()))
		detail := err.Error()
		if pe, ok := err.(*ProviderError); ok {
			detail = pe.Verbose()
		}
		triedVerbose = append(triedVerbose, fmt.Sprintf("%s: %s", cfg.Describe(), detail))
		if errors.Is(err, errSetAside) {
			continue
		}

		// A prompt refused on content grounds, or anything else not worth
		// repeating, stops here rather than spending the rest of the list.
		pe, ok := err.(*ProviderError)
		if !ok || !pe.Retryable() {
			if i == 0 {
				return "", cfg, err
			}
			break
		}
		// The caller gave up (page closed, request cancelled): stop asking.
		if ctx.Err() != nil {
			break
		}
	}

	return "", Config{}, &ChainError{
		Attempts: strings.Join(tried, " ⟵ "),
		Detail:   strings.Join(triedVerbose, " ⟵ "),
	}
}

// ChainError reports that every configured model failed, with a message for
// the page and a fuller one for the log.
type ChainError struct {
	Attempts string
	Detail   string
}

func (e *ChainError) Error() string {
	return "هیچ‌یک از سرویس‌های هوش مصنوعی پاسخ نداد: " + e.Attempts
}

// Verbose carries the transport detail an administrator needs.
func (e *ChainError) Verbose() string {
	return "هیچ‌یک از سرویس‌های هوش مصنوعی پاسخ نداد: " + e.Detail
}

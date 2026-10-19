// Package llm asks a language model a question: one of several providers
// (an OpenAI-compatible endpoint, Google Gemini, Anthropic Claude, or a model
// served on the same machine), tried in order until one answers, each behind
// a circuit breaker so a provider that is down is set aside instead of costing
// every question its whole timeout.
//
// It is the transport and nothing else. A caller brings its own system message
// and prompt, and decides what shape of answer it wants; this package gets the
// request to the provider, reads the reply in that provider's shape, and turns
// a failure into a typed error that says whether another provider might do
// better. That middle part is where the cost was: how the output budget is
// named per model family, which models refuse a temperature, an HTTP client
// that a misbehaving middlebox cannot break, and errors that never carry the
// key.
//
// Configuration is read from the environment (FromEnv, ChainFromEnv) so a key
// never ends up in a flag, a shell history or a file under version control;
// a Config can also be built directly. Nothing is sent anywhere until a
// provider is configured. The sentences a person may see -- errors, Describe --
// are in Persian, because that is who this was written for.
package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-ai/internal/netguard"
)

// httpClient carries every provider request, and it speaks HTTP/1.1 only.
//
// This is not a preference. Go's default transport offers h2 in the TLS
// handshake and uses it whenever the server agrees; api.openai.com agrees. On
// one office network the connection then died after about 250ms with
//
//	protocol error: received *http.http2WindowUpdateFrame before a SETTINGS frame
//
// which is a middlebox forwarding the stream without speaking the protocol
// properly. The identical request over HTTP/1.1, from the same machine in the
// same minute, returned 200 in about a second. Measured both ways before this
// was changed.
//
// The failure was invisible from outside, which is what made it expensive. The
// request never left the building, so the OpenAI account showed no usage at
// all -- the natural reading of which is "the feature does nothing" or "the key
// is wrong", and both are wrong.
//
// A non-nil but empty TLSNextProto is the documented way to switch h2 off: the
// transport stops advertising it in ALPN, so the negotiation never happens.
// The cost is an extra round trip on a connection opened a few times a day,
// against a feature that otherwise does not work at all.
//
// The dial hook refuses link-local, metadata and multicast addresses whatever
// the base URL's name resolved to (internal/netguard): somebody types that
// URL. The LAN and this machine stay open for a local model server and for a
// proxy.
//
// Timeout is a backstop above the longest per-request deadline (localTimeout):
// every call here also carries a context, but a call that ever lost it must
// still end.
var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
			Control: netguard.Control(netguard.Loopback),
		}).DialContext,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// Providers.
const (
	ProviderGemini = "gemini"
	// ProviderOpenAI is any service speaking the OpenAI chat-completions
	// shape, which is most of the free tiers -- Groq, OpenRouter, Mistral, a
	// model served on the LAN. It needs a base URL; there is no sensible
	// default, because guessing one would silently send data to a third party
	// the reader never chose.
	ProviderOpenAI = "openai"
)

// Limits.
const (
	// replyLimit bounds a reply's body: no answer worth having is larger, and
	// a misbehaving endpoint must not be read into memory without end.
	replyLimit = 64 << 10
	// askTimeout is the ceiling for a hosted API.
	//
	// Measured against a live API with a prompt of about five thousand
	// tokens, an answer took 15-20s at low reasoning effort and 29-34s at the
	// default, so a ceiling of 45s was close enough to the observed range to
	// start failing on a slow day.
	askTimeout = 2 * time.Minute
	// localTimeout suits a model running on the same machine with no GPU,
	// which is a completely different proposition: a few hundred Persian
	// tokens out of a 3B model on CPU is tens of seconds of arithmetic, not a
	// network round trip. Applying the hosted timeout to it would abort every
	// request just before it succeeded; a 900-token Persian answer took 60-90s.
	//
	// Keep it below the WriteTimeout of any HTTP server that waits on it. Go
	// resets that write deadline when the request header is read, so it caps
	// the whole handler -- a client budget above it is unreachable, and the
	// request dies as a dropped connection carrying no error text at all
	// rather than as a timeout the caller can explain.
	localTimeout = 3 * time.Minute
)

// Config is how to reach one model. FromEnv and ChainFromEnv read it from the
// environment -- never a flag, never a file in the repository -- so the key
// cannot end up in a shell history, a process listing or a commit. One built
// in code should pass through WithDefaults, which fills in what it leaves
// blank the way the environment's are.
type Config struct {
	Provider string
	Key      string
	Model    string
	BaseURL  string
}

// FromEnv reads the configuration: DOCFIND_AI_PROVIDER, DOCFIND_AI_KEY,
// DOCFIND_AI_MODEL and DOCFIND_AI_URL.
func FromEnv() Config {
	return normalize(Config{
		Provider: strings.ToLower(strings.TrimSpace(os.Getenv("DOCFIND_AI_PROVIDER"))),
		Key:      strings.TrimSpace(os.Getenv("DOCFIND_AI_KEY")),
		Model:    strings.TrimSpace(os.Getenv("DOCFIND_AI_MODEL")),
		BaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("DOCFIND_AI_URL")), "/"),
	})
}

// WithDefaults fills in what c leaves blank: the provider its key implies, and
// the address and model that provider's defaults imply.
func (c Config) WithDefaults() Config { return normalize(c) }

// normalize fills in what an entry leaves blank: the provider its key
// implies, and the address and model that provider's defaults imply.
//
// It applies to every chain entry, not just the primary. Numbered entries used
// to skip it, which was harmless while every fallback was another OpenAI model
// inheriting the primary's address -- and stops being harmless the moment the
// primary is a different provider, because then a fallback has nothing to
// inherit and needs its own provider's defaults.
func normalize(c Config) Config {
	// Anthropic's keys also start with "sk-" ("sk-ant-..."), so they have to
	// be recognised before the OpenAI rule below claims them.
	if c.Provider == "" && strings.HasPrefix(c.Key, "sk-ant-") {
		c.Provider = ProviderAnthropic
	}
	// An OpenAI key with no provider named used to fall straight through to
	// Gemini, because Gemini was the default from when this had one provider.
	// The consequence was invisible and expensive: the request went to Google
	// carrying an OpenAI key, and the OpenAI account showed no usage at all --
	// indistinguishable, from the outside, from the feature doing nothing.
	//
	// "sk-" is OpenAI's own key prefix and nothing else configured here uses
	// it, so it identifies the intended provider with no ambiguity. An explicit
	// DOCFIND_AI_PROVIDER still wins: this only fills in a blank.
	if c.Provider == "" && strings.HasPrefix(c.Key, "sk-") {
		c.Provider = ProviderOpenAI
	}
	if c.Provider == "" {
		c.Provider = ProviderGemini
	}
	// "openai" with a key but no address means the OpenAI account. It cannot
	// mean anything else: a local relay is always given its address, because
	// that address is the only way to find it.
	//
	// This was not merely unset-and-broken, it was unset-and-OFF. Enabled()
	// tests the base URL rather than the key for this provider -- deliberately,
	// so a relay holding its own credentials can be used without inventing a
	// fake key -- which meant a correct-looking .env with a provider and a key
	// and no URL disabled the whole feature.
	if c.Provider == ProviderOpenAI && c.BaseURL == "" && c.Key != "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	if c.Provider == ProviderAnthropic && c.BaseURL == "" {
		c.BaseURL = anthropicBaseURL
	}
	if c.Model == "" {
		switch c.Provider {
		case ProviderGemini:
			c.Model = "gemini-2.0-flash"
		case ProviderOpenAI:
			c.Model = defaultOpenAIModel(c.BaseURL)
		case ProviderAnthropic:
			c.Model = defaultAnthropicModel
		}
	}
	return c
}

// defaultOpenAIModel picks a model name that the configured endpoint will
// actually recognise.
//
// "openai" here means the OpenAI-compatible API shape, not OpenAI the company:
// the same code talks to Groq, to a local relay, and to api.openai.com. The
// default used to be "llama-3.3-70b-versatile" for all of them, which is a Groq
// model name -- so pointing this at api.openai.com with a real key and no
// explicit model produced "model not found" on every request. The default has
// to follow the endpoint.
//
// Chosen for api.openai.com: gpt-5-mini. On a prompt of roughly seven thousand
// input tokens and fourteen hundred out -- a long question with its context --
// that is about half a cent a call, so a five-dollar balance is on the order
// of a thousand questions. The nano tier is four times cheaper again and
// is the right thing to set while proving the wiring works; the full-size
// models are five to seven times dearer for a question this shape does not
// need. All of it is overridden by DOCFIND_AI_MODEL.
// reasoningBudget is the ceiling on thinking plus answer for a reasoning model.
//
// Sized from measurement, not from the answer's length: a prompt of about five
// thousand tokens on gpt-5-mini at reasoning_effort "low" used ~1,800 completion tokens in
// total, so 8,000 is roughly four times the observed need. That headroom is
// almost free, because this parameter is a ceiling and not an allocation --
// the request is billed for the tokens actually produced, and every measured
// run finished well inside it. Raising the ceiling costs nothing; hitting it
// costs the entire answer.
const reasoningBudget = 8000

// reasoningFamily reports whether a model expects the newer request shape.
//
// OpenAI's reasoning models -- the gpt-5 generation and the o-series -- renamed
// max_tokens to max_completion_tokens and reject the old spelling outright, and
// several refuse any temperature other than the default. Older chat models want
// max_tokens and accept a temperature. There is no capability endpoint to ask,
// so this goes by the family in the name, which is how the two request shapes
// are actually distinguished in practice.
//
// Matched on a prefix rather than an exact list, deliberately: a list would be
// wrong the week a new point release appears, and the failure mode of guessing
// wrong here is every request returning 400 rather than a slightly worse answer.
// Anything unrecognised falls through to the older shape, which is what every
// non-OpenAI compatible endpoint (Groq, a local relay) also expects.
func reasoningFamily(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, p := range []string{"gpt-5", "gpt-6", "o1", "o3", "o4"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

func defaultOpenAIModel(baseURL string) string {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, "groq.com"):
		return "llama-3.3-70b-versatile"
	case strings.Contains(u, "openai.com"):
		return "gpt-5-mini"
	default:
		// A local relay or an unknown compatible endpoint: it serves whatever
		// it was configured with, and naming a hosted model would be a guess.
		// Left empty so the endpoint's own default applies and a wrong guess
		// cannot be the reason a request fails.
		return ""
	}
}

// Enabled reports whether a model is configured. Nothing is sent until it is:
// an unconfigured Config is refused rather than reaching for a network it was
// never told about.
func (c Config) Enabled() bool {
	switch c.Provider {
	case ProviderGemini, ProviderAnthropic:
		// Hosted only, and nothing to ask without the account's key.
		return c.Key != ""
	case ProviderOpenAI:
		// A base URL is enough here, and the key is optional. Local routing
		// proxies hold the provider credentials themselves and document the
		// client key as
		// "unused"; demanding one would mean inventing a fake value to get
		// past this check. Setting DOCFIND_AI_URL is explicit enough to count
		// as switching the feature on.
		return c.BaseURL != ""
	}
	return false
}

// IsLocal reports whether this entry points at a model on this machine.
//
// It is worth distinguishing because almost every operational assumption
// differs: latency is in tens of seconds rather than seconds, no key is
// needed, and nothing leaves the building.
func (c Config) IsLocal() bool {
	if c.BaseURL == "" {
		return false
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" {
		return true
	}
	// Covers the whole 127/8 loopback range, not just 127.0.0.1.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Timeout is how long to wait for an answer.
//
// DOCFIND_AI_TIMEOUT (in seconds) overrides everything, for the case nobody
// anticipated. Otherwise a local model gets the long timeout and a hosted one
// the short: a single constant cannot serve both, and the failure it produces
// -- a request cancelled seconds before it would have returned -- looks
// exactly like the model being broken.
func (c Config) Timeout() time.Duration {
	if s := strings.TrimSpace(os.Getenv("DOCFIND_AI_TIMEOUT")); s != "" {
		// Bare seconds first, because that is what the variable is documented
		// as. But a Go programmer setting a timeout writes "180s" or "3m", and
		// silently ignoring that spelling turns the one escape hatch here into
		// a no-op that looks like it worked.
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	if c.IsLocal() {
		return localTimeout
	}
	if c.Provider == ProviderAnthropic {
		return anthropicTimeout
	}
	return askTimeout
}

// Describe names the provider and model for the page, and deliberately not
// the key. It is shown to the reader so that "this left the building" is
// visible in the interface rather than buried in a config file.
// Endpoint is the address this configuration will actually be sent to.
//
// Separate from Describe because they answer different questions for different
// readers. Describe is for the page and deliberately hides the wire format of
// a local model. This is for the log, where the only question worth answering
// is "where is the key going" -- which is exactly the question nothing could
// answer when an OpenAI key was resolving to Gemini and the log said, quite
// truthfully, "gemini / gemini-2.0-flash".
func (c Config) Endpoint() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	if c.Provider == ProviderGemini {
		return "https://generativelanguage.googleapis.com/v1beta"
	}
	return "(no address configured)"
}

func (c Config) Describe() string {
	if !c.Enabled() {
		return ""
	}
	// A local model is named as local, not by the wire format it speaks.
	// Calling it "openai / qwen2.5:3b" because it talks the OpenAI shape is
	// actively misleading: it reads as though the data went to
	// OpenAI when in fact nothing left the machine.
	if c.IsLocal() {
		return "مدل محلی سرور (" + c.Model + ")"
	}
	return c.Provider + " / " + c.Model
}

// geminiRequestWith builds a Gemini request with the output budget as a
// parameter.
func geminiRequestWith(ctx context.Context, cfg Config, prompt string, maxTokens int) (*http.Request, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:generateContent", base, cfg.Model)

	body, err := json.Marshal(map[string]any{
		"contents": []any{map[string]any{
			"parts": []any{map[string]string{"text": prompt}},
		}},
		"generationConfig": map[string]any{
			"temperature":     0.4,
			"maxOutputTokens": maxTokens,
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The key goes in a header, not the query string as Google's examples
	// show. A key in a URL is written to every proxy log and browser history
	// it passes through, and this request may well cross a proxy.
	req.Header.Set("x-goog-api-key", cfg.Key)
	req.Header.Set("User-Agent", "docfind-ai/1.0")
	return req, nil
}

// replyText reads a successful reply in whichever shape its provider uses.
func replyText(cfg Config, body []byte) (string, error) {
	switch cfg.Provider {
	case ProviderGemini:
		return geminiText(body)
	case ProviderOpenAI:
		return openAIText(body)
	case ProviderAnthropic:
		return anthropicText(body)
	}
	return "", fmt.Errorf("سرویس هوش مصنوعی ناشناخته است: %s", cfg.Provider)
}

// openAIRequestWith builds a chat-completions request: the caller's system
// message and prompt, and an output budget for the answer.
func openAIRequestWith(ctx context.Context, cfg Config, system, prompt string,
	maxTokens int) (*http.Request, error) {
	payload := map[string]any{
		"model": cfg.Model,
		"messages": []any{
			map[string]string{"role": "system", "content": system},
			map[string]string{"role": "user", "content": prompt},
		},
	}
	// How the output budget is named, and whether a temperature is accepted,
	// depends on the model family -- and getting it wrong is not a degraded
	// answer, it is a 400 on every single request.
	//
	// The gpt-5 and o-series reasoning models renamed max_tokens to
	// max_completion_tokens and reject the old name; several of them also
	// refuse any temperature but the default. Everything older takes
	// max_tokens and honours temperature. Sending the pair that matches the
	// model is the difference between the feature working and the feature
	// never once succeeding.
	if reasoningFamily(cfg.Model) {
		// A reasoning model spends its hidden reasoning out of the SAME budget
		// as the visible answer, and is charged for both. So
		// max_completion_tokens is not "how long may the answer be", it is "how
		// much thinking plus answer is allowed" -- and when the thinking uses it
		// all, the answer comes back as an empty string with finish_reason
		// "length" and no error of any kind.
		//
		// That is exactly what happened. Measured against a real prompt of
		// 5,194 input tokens on gpt-5-mini:
		//
		//   budget  effort    finish  reasoning  answer chars
		//   1,400   default   length      1,400             0
		//   4,000   default   stop        1,600         4,853
		//   8,000   default   stop        1,536         4,292
		//   8,000   low       stop          128         4,954
		//
		// The 1,400 was tuned against older models, where the whole budget goes
		// to the answer. Under a reasoning model it produced nothing at all, every
		// time, while still being billed for the 1,400 tokens it burned.
		//
		// "low" is not a compromise: it won on every axis measured -- fastest
		// (20s against 29-34s), cheapest, and the longest answer of the four.
		// What most questions sent this way want is reading and grouping, which
		// is not what extended reasoning is for.
		payload["max_completion_tokens"] = reasoningBudget
		payload["reasoning_effort"] = "low"
	} else {
		payload["max_tokens"] = maxTokens
		// Low, not zero: the answer should be stable enough that two readers
		// asking the same question see the same answer, without being so rigid
		// that it repeats one phrasing for everything.
		payload["temperature"] = 0.4
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Omitted entirely when there is no key, rather than sent as an empty
	// bearer token: a local relay holding its own credentials rejects a
	// malformed Authorization header that it would have ignored if absent.
	if cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Key)
	}
	req.Header.Set("User-Agent", "docfind-ai/1.0")
	return req, nil
}

func geminiText(body []byte) (string, error) {
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("پاسخ سرویس هوش مصنوعی قابل خواندن نبود: %w", err)
	}
	if out.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("سرویس هوش مصنوعی درخواست را رد کرد: %s",
			out.PromptFeedback.BlockReason)
	}
	var b strings.Builder
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

func openAIText(body []byte) (string, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens        int `json:"completion_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("پاسخ سرویس هوش مصنوعی قابل خواندن نبود: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("سرویس هوش مصنوعی هیچ پاسخی برنگرداند")
	}
	text := out.Choices[0].Message.Content

	// A reasoning model that runs out of budget returns HTTP 200, an empty
	// string, and finish_reason "length". Nothing about that says "your token
	// ceiling is too low" -- and the generic "empty answer" message that used
	// to be shown here sent the reader looking at their key and their network,
	// which were both fine. It cost a day. Say the actual cause.
	if strings.TrimSpace(text) == "" && out.Choices[0].FinishReason == "length" {
		return "", fmt.Errorf("مدل همهٔ سقف توکن را صرف استدلال کرد و متنی باقی نماند "+
			"(%d توکن، %d توکن استدلال). سقف max_completion_tokens باید بالاتر برود یا "+
			"reasoning_effort پایین‌تر بیاید",
			out.Usage.CompletionTokens, out.Usage.CompletionTokensDetails.ReasoningTokens)
	}
	return text, nil
}

// scrub keeps the key out of an error. A transport error carries the request
// URL, and a provider that wants the key as a query parameter would put it
// there -- straight into a log file that is not treated as a secret.
func scrub(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	if s := err.Error(); strings.Contains(s, key) {
		return errors.New(strings.ReplaceAll(s, key, "[کلید حذف شد]"))
	}
	return err
}

func redactKey(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[کلید حذف شد]")
}

// providerError pulls the human-readable reason out of a failure body.
//
// The naive version took the first line, which was actively harmful: OpenAI
// pretty-prints its errors, so the first line of a real 429 is the single
// character "{" and the reader was shown «HTTP 429): {» -- a dead end that
// hides the one sentence that says what to do. The actual message there was
// "You have no credits remaining", which is entirely actionable.
//
// So the JSON shape every one of these providers uses is tried first, and the
// one-line squash is kept only as the fallback for a proxy or gateway that
// returns HTML.
func providerError(body []byte) string {
	var out struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err == nil && out.Error.Message != "" {
		msg := out.Error.Message
		// The code is what distinguishes "out of credit" from "too many
		// requests just now", and both arrive as 429.
		if c := out.Error.Code; c != "" {
			msg += " [" + c + "]"
		} else if t := out.Error.Type; t != "" {
			msg += " [" + t + "]"
		}
		return clip(msg)
	}
	s := strings.TrimSpace(string(body))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return clip(s)
}

func clip(s string) string {
	const max = 300
	if len(s) > max {
		return strings.ToValidUTF8(s[:max], "") + "…"
	}
	return s
}

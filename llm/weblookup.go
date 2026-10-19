package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Questions answered from the web: the model searches, reads, and answers,
// naming the pages it used.
//
// Two of the configured providers can: OpenAI through its Responses API with
// the web_search tool, and Gemini grounded with Google Search. A model on this
// machine cannot, and neither can an OpenAI-shaped relay that is not OpenAI --
// their /chat/completions has no search -- so SearchChain passes them by
// rather than asking them to make an address up.
//
// Everything below the request itself is Complete's: the chain, the circuit
// breakers, the key redaction, the typed ProviderError.

// ErrNoSearch is a chain with nothing in it that can search the web.
var ErrNoSearch = errors.New("هیچ‌یک از مدل‌های پیکربندی‌شده جست‌وجوی اینترنت ندارد " +
	"(کلید OpenAI روی api.openai.com یا کلید Gemini لازم است)")

// CanSearch reports whether cfg can search the web.
func (c Config) CanSearch() bool {
	if !c.Enabled() {
		return false
	}
	switch c.Provider {
	case ProviderGemini:
		return true
	case ProviderOpenAI:
		return strings.HasPrefix(strings.ToLower(c.BaseURL), "https://api.openai.com/")
	}
	return false
}

// searchTimeout: a search reads several pages before it answers.
const searchTimeout = 120 * time.Second

// SearchChain asks the first entry that can search, and the next if it fails,
// and returns the answer, the pages it cited, and which entry answered.
func SearchChain(ctx context.Context, chain []Config, system, prompt string) (string, []Source, Config, error) {
	var able []Config
	for _, c := range chain {
		if c.CanSearch() {
			able = append(able, c)
		}
	}
	if len(able) == 0 {
		return "", nil, Config{}, ErrNoSearch
	}
	var sources []Source
	text, used, err := tryChain(ctx, able, func(ctx context.Context, cfg Config) (string, error) {
		t, s, err := Search(ctx, cfg, system, prompt)
		sources = s
		return t, err
	})
	return text, sources, used, err
}

// Source is a page an answer rests on.
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// Search asks one model to answer from the web.
func Search(ctx context.Context, cfg Config, system, prompt string) (string, []Source, error) {
	if !cfg.CanSearch() {
		return "", nil, ErrNoSearch
	}
	c, cancel := context.WithTimeout(ctx, max(cfg.Timeout(), searchTimeout))
	defer cancel()
	if cfg.Provider == ProviderGemini {
		return search(c, cfg, geminiSearchRequest(cfg, system, prompt), geminiSearchText)
	}
	text, sources, err := search(c, cfg, openAISearchRequest(cfg, system, prompt, "web_search"), openAISearchText)
	// An account on the older name of the tool refuses the newer one.
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Status == http.StatusBadRequest && strings.Contains(pe.Msg, "web_search") {
		return search(c, cfg, openAISearchRequest(cfg, system, prompt, "web_search_preview"), openAISearchText)
	}
	return text, sources, err
}

type searchRequest struct {
	url    string
	body   []byte
	header map[string]string
}

func search(ctx context.Context, cfg Config, r searchRequest,
	read func([]byte) (string, []Source, error)) (string, []Source, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(r.body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "docfind-ai/1.0")
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, &ProviderError{Transport: true, Local: cfg.IsLocal(), Provider: cfg.Provider,
			Model: cfg.Model, Msg: scrub(err, cfg.Key).Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, replyLimit))
	if err != nil {
		return "", nil, &ProviderError{Transport: true, Local: cfg.IsLocal(), Provider: cfg.Provider,
			Model: cfg.Model, Msg: scrub(err, cfg.Key).Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, &ProviderError{Status: resp.StatusCode, Provider: cfg.Provider, Model: cfg.Model,
			Msg: redactKey(providerError(body), cfg.Key)}
	}
	text, sources, err := read(body)
	if err != nil {
		return "", nil, err
	}
	if text = strings.TrimSpace(text); text == "" {
		return "", nil, &ProviderError{Empty: true, Provider: cfg.Provider, Model: cfg.Model}
	}
	return text, sources, nil
}

func openAISearchRequest(cfg Config, system, prompt, tool string) searchRequest {
	payload := map[string]any{
		"model":        cfg.Model,
		"instructions": system,
		"input":        prompt,
		"tools":        []any{map[string]string{"type": tool}},
	}
	if reasoningFamily(cfg.Model) {
		payload["reasoning"] = map[string]string{"effort": "low"}
	}
	body, _ := json.Marshal(payload)
	h := map[string]string{}
	if cfg.Key != "" {
		h["Authorization"] = "Bearer " + cfg.Key
	}
	return searchRequest{url: cfg.BaseURL + "/responses", body: body, header: h}
}

// openAISearchText reads a Responses answer: the text of its messages, and the
// pages their url_citation annotations name.
func openAISearchText(body []byte) (string, []Source, error) {
	var out struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Annotations []struct {
					Type  string `json:"type"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"annotations"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", nil, errors.New("پاسخ سرویس هوش مصنوعی قابل خواندن نبود: " + err.Error())
	}
	var b strings.Builder
	var sources []Source
	for _, o := range out.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			if c.Type != "output_text" {
				continue
			}
			b.WriteString(c.Text)
			for _, a := range c.Annotations {
				if a.Type == "url_citation" && a.URL != "" {
					sources = addSource(sources, Source{URL: a.URL, Title: a.Title})
				}
			}
		}
	}
	return b.String(), sources, nil
}

func geminiSearchRequest(cfg Config, system, prompt string) searchRequest {
	base := cfg.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	body, _ := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]string{"text": system}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": prompt}}}},
		"tools":             []any{map[string]any{"google_search": map[string]any{}}},
	})
	// The key in a header, as for every other Gemini request (geminiRequestWith).
	return searchRequest{url: base + "/models/" + cfg.Model + ":generateContent", body: body,
		header: map[string]string{"x-goog-api-key": cfg.Key}}
}

// geminiSearchText reads a grounded answer: its text, and the pages its
// grounding metadata names.
func geminiSearchText(body []byte) (string, []Source, error) {
	text, err := geminiText(body)
	if err != nil {
		return "", nil, err
	}
	var out struct {
		Candidates []struct {
			GroundingMetadata struct {
				GroundingChunks []struct {
					Web struct {
						URI   string `json:"uri"`
						Title string `json:"title"`
					} `json:"web"`
				} `json:"groundingChunks"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
	}
	_ = json.Unmarshal(body, &out)
	var sources []Source
	for _, c := range out.Candidates {
		for _, g := range c.GroundingMetadata.GroundingChunks {
			if g.Web.URI != "" {
				sources = addSource(sources, Source{URL: g.Web.URI, Title: g.Web.Title})
			}
		}
	}
	return text, sources, nil
}

func addSource(list []Source, s Source) []Source {
	for _, have := range list {
		if have.URL == s.URL {
			return list
		}
	}
	if len(list) >= 8 {
		return list
	}
	return append(list, s)
}

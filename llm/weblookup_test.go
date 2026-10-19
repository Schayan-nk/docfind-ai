package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Only a model that can search is asked: OpenAI itself, or Gemini. A model on
// this machine, or a relay speaking OpenAI's chat shape, would make an
// address up rather than find it.
func TestOnlyAModelThatCanSearchIsAsked(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want bool
	}{
		{Config{Provider: ProviderOpenAI, Key: "sk-x", Model: "gpt-4.1-mini", BaseURL: "https://api.openai.com/v1"}, true},
		{Config{Provider: ProviderGemini, Key: "g", Model: "gemini-2.5-flash"}, true},
		{Config{Provider: ProviderOpenAI, Model: "docfind-fa", BaseURL: "http://127.0.0.1:11434/v1"}, false},
		{Config{Provider: ProviderOpenAI, Key: "x", Model: "m", BaseURL: "https://api.groq.com/openai/v1"}, false},
		{Config{Provider: ProviderOpenAI, Model: "m"}, false},
	} {
		if got := c.cfg.CanSearch(); got != c.want {
			t.Errorf("%s at %q: %v, want %v", c.cfg.Provider, c.cfg.BaseURL, got, c.want)
		}
	}
	local := []Config{{Provider: ProviderOpenAI, Model: "docfind-fa", BaseURL: "http://127.0.0.1:11434/v1"}}
	if _, _, _, err := SearchChain(context.Background(), local, "s", "p"); !errors.Is(err, ErrNoSearch) {
		t.Errorf("a local-only chain: %v", err)
	}
}

// OpenAI: the Responses API with the web_search tool, the key as a bearer
// token; the answer's text and the pages its citations name. An account that
// knows only the tool's older name is asked again with it.
func TestAnOpenAISearchReadsTheAnswerAndItsPages(t *testing.T) {
	var tools []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model, Instructions, Input string
			Tools                      []struct{ Type string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sk-test" ||
			body.Instructions != "system" || body.Input != "prompt" || len(body.Tools) != 1 {
			t.Errorf("request %s %+v", r.URL.Path, body)
		}
		tools = append(tools, body.Tools[0].Type)
		if body.Tools[0].Type == "web_search" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"Tool 'web_search' is not supported with this model.","type":"invalid_request_error"}}`))
			return
		}
		w.Write([]byte(`{"output":[{"type":"web_search_call","status":"completed"},
			{"type":"message","content":[{"type":"output_text","text":"{\"found\":true}",
			 "annotations":[{"type":"url_citation","url":"https://example.ir/contact","title":"تماس با ما"},
			                {"type":"url_citation","url":"https://example.ir/contact","title":"again"}]}]}]}`))
	}))
	defer srv.Close()
	cfg := Config{Provider: ProviderOpenAI, Key: "sk-test", Model: "gpt-4.1-mini", BaseURL: srv.URL + "/v1"}
	text, sources, err := search(context.Background(), cfg, openAISearchRequest(cfg, "system", "prompt", "web_search"), openAISearchText)
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != 400 || !strings.Contains(pe.Msg, "web_search") {
		t.Fatalf("the first attempt: %v", err)
	}
	text, sources, err = search(context.Background(), cfg, openAISearchRequest(cfg, "system", "prompt", "web_search_preview"), openAISearchText)
	if err != nil || text != `{"found":true}` || len(sources) != 1 || sources[0].URL != "https://example.ir/contact" {
		t.Errorf("text %q, sources %+v, err %v", text, sources, err)
	}
	if strings.Join(tools, ",") != "web_search,web_search_preview" {
		t.Errorf("tools asked for: %v", tools)
	}
}

// Gemini: grounded with Google Search, the key in its header; the pages come
// from the grounding metadata.
func TestAGeminiSearchReadsTheAnswerAndItsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if r.URL.Path != "/models/gemini-2.5-flash:generateContent" || r.Header.Get("x-goog-api-key") != "g-key" ||
			len(tools) != 1 || strings.Contains(r.URL.RawQuery, "g-key") {
			t.Errorf("request %s %v", r.URL, body)
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"found\":false}"}]},
			"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://vertexaisearch.cloud.google.com/r/1","title":"example.ir"}}]}}]}`))
	}))
	defer srv.Close()
	cfg := Config{Provider: ProviderGemini, Key: "g-key", Model: "gemini-2.5-flash", BaseURL: srv.URL}
	text, sources, err := search(context.Background(), cfg, geminiSearchRequest(cfg, "s", "p"), geminiSearchText)
	if err != nil || text != `{"found":false}` || len(sources) != 1 || sources[0].Title != "example.ir" {
		t.Errorf("text %q, sources %+v, err %v", text, sources, err)
	}
}

package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Costs API's pages are all read and summed, with the admin key as a
// bearer token and the window asked for in daily buckets.
func TestOrgCostsSumsEveryPage(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer sk-admin-test" {
			t.Errorf("Authorization = %q", got)
		}
		q := r.URL.Query()
		if q.Get("start_time") != "1790812800" || q.Get("bucket_width") != "1d" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("page") == "" {
			w.Write([]byte(`{"object":"page","data":[
				{"object":"bucket","results":[{"amount":{"value":1.25,"currency":"usd"}}]},
				{"object":"bucket","results":[{"amount":{"value":0.75,"currency":"usd"}},{"amount":{"value":9,"currency":"eur"}}]}],
				"has_more":true,"next_page":"p2"}`))
			return
		}
		if q.Get("page") != "p2" {
			t.Errorf("page = %q", q.Get("page"))
		}
		w.Write([]byte(`{"object":"page","data":[{"object":"bucket","results":[{"amount":{"value":2,"currency":"usd"}}]}],"has_more":false,"next_page":null}`))
	}))
	defer srv.Close()
	old := CostsURL
	CostsURL = srv.URL
	defer func() { CostsURL = old }()

	got, err := OrgCosts(context.Background(), "sk-admin-test", since)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 || calls != 2 {
		t.Fatalf("total %v over %d calls, want 4 over 2 (a non-USD amount is not added)", got, calls)
	}
}

// A refusal says what OpenAI said, and never the key.
func TestOrgCostsRefusalKeepsTheKeyOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-admin-secret123","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()
	old := CostsURL
	CostsURL = srv.URL
	defer func() { CostsURL = old }()

	_, err := OrgCosts(context.Background(), "sk-admin-secret123", time.Now())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret123") {
		t.Fatalf("err = %v", err)
	}
	if _, err := OrgCosts(context.Background(), "", time.Now()); err == nil {
		t.Fatal("no admin key must be an error, not a zero spend")
	}
}

// Out of credit and too many requests both arrive as 429; only the first is
// the credit being gone.
func TestQuotaSpentTellsCreditFromRateLimit(t *testing.T) {
	for _, c := range []struct {
		err   error
		spent bool
	}{
		{&ProviderError{Status: 429, Msg: "You exceeded your current quota, please check your plan and billing details. [insufficient_quota]"}, true},
		{&ProviderError{Status: 429, Msg: "You have no credits remaining [credit_balance_exhausted]"}, true},
		{&ProviderError{Status: 429, Msg: "Rate limit reached for gpt-4.1-mini [rate_limit_exceeded]"}, false},
		{&ProviderError{Status: 402, Msg: "payment required"}, true},
		{&ProviderError{Status: 401, Msg: "Incorrect API key [invalid_api_key]"}, false},
		{nil, false},
	} {
		if got := QuotaSpent(c.err); got != c.spent {
			t.Errorf("QuotaSpent(%v) = %v", c.err, got)
		}
	}
	if !KeyRefused(&ProviderError{Status: 401}) || KeyRefused(&ProviderError{Status: 429}) {
		t.Error("KeyRefused should be 401/403 only")
	}
}

// Only OpenAI's own platform has an OpenAI balance; a local model behind the
// same API does not.
func TestIsOpenAIPlatform(t *testing.T) {
	if !IsOpenAIPlatform(Config{Provider: ProviderOpenAI, BaseURL: "https://api.openai.com/v1"}) {
		t.Error("api.openai.com is the platform")
	}
	if IsOpenAIPlatform(Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:11434/v1"}) {
		t.Error("a local Ollama is not")
	}
	if IsOpenAIPlatform(Config{Provider: ProviderGemini, BaseURL: "https://api.openai.com/v1"}) {
		t.Error("a Gemini entry is not")
	}
}

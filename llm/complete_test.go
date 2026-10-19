package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The plain completion call.
//
// What matters here is what it does NOT do: it must send the caller's system
// message and prompt and nothing else. Ask adds a page of commercial doctrine
// and three required headings, and strips those headings back out of the
// reply; a caller asking for a transliterated company name wants none of that.

func TestCompleteSendsOnlyWhatItWasGiven(t *testing.T) {
	var gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		all, _ := io.ReadAll(r.Body)
		gotBody = string(all)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"1. نیکان"}}]}`))
	}))
	defer srv.Close()

	cfg := Config{Provider: ProviderOpenAI, Key: "tok", Model: "m", BaseURL: srv.URL}
	out, err := Complete(context.Background(), cfg, "SYSTEM-MARKER", "PROMPT-MARKER")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "1. نیکان" {
		t.Errorf("answer came back as %q", out)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization header was %q", gotAuth)
	}
	for _, want := range []string{"SYSTEM-MARKER", "PROMPT-MARKER"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("%s did not reach the request body", want)
		}
	}
	// THE DOCTRINE MUST NOT BE THERE. These are words the advice prompt
	// carries; their presence would mean Complete is routing through Problem.
	for _, unwanted := range []string{"تفسیر", "اقدام‌ها", "چرخه", "بازرگانی"} {
		if strings.Contains(gotBody, unwanted) {
			t.Errorf("the advice doctrine leaked into a plain completion: %q", unwanted)
		}
	}
}

// Gemini has no separate system field, so the instruction has to ride at the
// top of the prompt or it is silently dropped.
func TestCompleteFoldsTheSystemMessageIntoAGeminiPrompt(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		all, _ := io.ReadAll(r.Body)
		gotBody = string(all)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"نیکان"}]}}]}`))
	}))
	defer srv.Close()

	cfg := Config{Provider: ProviderGemini, Key: "tok", Model: "m", BaseURL: srv.URL}
	if _, err := Complete(context.Background(), cfg, "SYSTEM-MARKER", "PROMPT-MARKER"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for _, want := range []string{"SYSTEM-MARKER", "PROMPT-MARKER"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("%s did not reach the Gemini request", want)
		}
	}
}

// An answer of nothing is typed, so a chain treats it as worth trying
// elsewhere rather than as a successful empty reply.
func TestCompleteTreatsAnEmptyAnswerAsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"   "}}]}`))
	}))
	defer srv.Close()

	cfg := Config{Provider: ProviderOpenAI, Key: "tok", Model: "m", BaseURL: srv.URL}
	_, err := Complete(context.Background(), cfg, "s", "p")
	if err == nil {
		t.Fatal("an empty answer was returned as success")
	}
	pe, ok := err.(*ProviderError)
	if !ok || !pe.Empty {
		t.Errorf("error is %T (%v); want a *ProviderError with Empty set", err, err)
	}
}

// A provider error must not carry the key, on this path as much as on the
// advice one.
func TestCompleteDoesNotLeakTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret-value-here"}}`))
	}))
	defer srv.Close()

	const key = "sk-secret-value-here"
	cfg := Config{Provider: ProviderOpenAI, Key: key, Model: "m", BaseURL: srv.URL}
	_, err := Complete(context.Background(), cfg, "s", "p")
	if err == nil {
		t.Fatal("a 401 was returned as success")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the key is in the error: %v", err)
	}
}

func TestCompleteRefusesWhenUnconfigured(t *testing.T) {
	if _, err := Complete(context.Background(), Config{}, "s", "p"); err == nil {
		t.Error("an unconfigured Complete was accepted")
	}
}

func TestCompleteRefusesAnEmptyPrompt(t *testing.T) {
	cfg := Config{Provider: ProviderOpenAI, Key: "tok", Model: "m", BaseURL: "http://127.0.0.1:1"}
	if _, err := Complete(context.Background(), cfg, "s", "   \n "); err == nil {
		t.Error("an empty prompt was sent to a model")
	}
}

// The chain wrapper falls through to the second entry, which is the whole
// reason it exists rather than calling Complete directly.
func TestCompleteChainFallsThroughToTheNextModel(t *testing.T) {
	var hits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream is down"}}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"نیکان"}}]}`))
	}))
	defer good.Close()

	chain := []Config{
		{Provider: ProviderOpenAI, Key: "a", Model: "first", BaseURL: bad.URL},
		{Provider: ProviderOpenAI, Key: "b", Model: "second", BaseURL: good.URL},
	}
	out, used, err := CompleteChain(context.Background(), chain, "s", "p")
	if err != nil {
		t.Fatalf("CompleteChain: %v", err)
	}
	if out != "نیکان" {
		t.Errorf("answer is %q", out)
	}
	if used.Model != "second" {
		t.Errorf("answered by %q, want second", used.Model)
	}
	if hits != 1 {
		t.Errorf("the failing entry was called %d times", hits)
	}
}

func TestCompleteChainWithNoModelsIsAnError(t *testing.T) {
	if _, _, err := CompleteChain(context.Background(), nil, "s", "p"); err == nil {
		t.Error("an empty chain was accepted")
	}
}

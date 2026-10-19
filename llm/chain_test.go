package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okServer answers like an OpenAI-compatible service.
func okServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"` + reply + `"}}]}`))
	}))
}

func failServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func cfg(url, model string) Config {
	return Config{Provider: ProviderOpenAI, Key: "k", Model: model, BaseURL: url}
}

// A manager reading the analysis page should not be shown a dial error.
//
// The real one was: «dial tcp 127.0.0.1:11434: connectex: No connection could
// be made because the target machine actively refused it». It tells a manager
// nothing they can act on, while the sentence in front of it already says the
// local service is not running. The detail still has to survive for the log.
func TestTransportDetailStaysOutOfTheReadersMessage(t *testing.T) {
	e := &ProviderError{
		Transport: true, Local: true, Provider: ProviderOpenAI, Model: "docfind-fa",
		Msg: `Post "http://127.0.0.1:11434/v1/chat/completions": dial tcp ` +
			`127.0.0.1:11434: connectex: No connection could be made`,
	}
	if strings.Contains(e.Error(), "dial tcp") {
		t.Errorf("the reader's message carries the dial error: %q", e.Error())
	}
	if !strings.Contains(e.Error(), "سرور") {
		t.Errorf("the reader's message does not say the local server is at fault: %q", e.Error())
	}
	if !strings.Contains(e.Verbose(), "dial tcp") {
		t.Errorf("the log lost the transport detail: %q", e.Verbose())
	}

	// A provider's own message is the opposite case: "no credits remaining"
	// is exactly what the reader needs, so it must NOT be trimmed.
	q := &ProviderError{Status: 429, Msg: "You have no credits remaining. [credit_balance_exhausted]"}
	if !strings.Contains(q.Error(), "no credits remaining") {
		t.Errorf("an actionable provider message was trimmed away: %q", q.Error())
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		e    ProviderError
		want bool
	}{
		{ProviderError{Transport: true}, true},
		{ProviderError{Empty: true}, true},
		{ProviderError{Status: 429}, true}, // out of quota or rate limited
		{ProviderError{Status: 401}, true}, // wrong key for this provider
		{ProviderError{Status: 404}, true}, // model not on this account
		{ProviderError{Status: 503}, true}, // provider down
		{ProviderError{Status: 400}, false},
		{ProviderError{Status: 422}, false},
	}
	for _, c := range cases {
		if got := c.e.Retryable(); got != c.want {
			t.Errorf("%+v: Retryable() = %v, want %v", c.e, got, c.want)
		}
	}
}

// A comma-separated model list is the cheapest way to say "this key, these
// models, in this order".
func TestModelListExpandsIntoSeparateEntries(t *testing.T) {
	t.Setenv("DOCFIND_AI_PROVIDER", ProviderOpenAI)
	t.Setenv("DOCFIND_AI_URL", "http://example.invalid/v1")
	t.Setenv("DOCFIND_AI_KEY", "k")
	t.Setenv("DOCFIND_AI_MODEL", "a, b ,c")
	for i := 2; i <= 5; i++ {
		t.Setenv("DOCFIND_AI_MODEL_"+string(rune('0'+i)), "")
	}

	chain := ChainFromEnv()
	if len(chain) != 3 {
		t.Fatalf("expected three entries, got %d: %+v", len(chain), chain)
	}
	for i, want := range []string{"a", "b", "c"} {
		if chain[i].Model != want {
			t.Errorf("entry %d is %q, want %q", i, chain[i].Model, want)
		}
	}
}

// A numbered fallback inherits what it does not set, so adding a second model
// on the same account does not mean repeating the key.
func TestNumberedFallbackInheritsFromPrimary(t *testing.T) {
	t.Setenv("DOCFIND_AI_PROVIDER", ProviderOpenAI)
	t.Setenv("DOCFIND_AI_URL", "http://primary.invalid/v1")
	t.Setenv("DOCFIND_AI_KEY", "primary-key")
	t.Setenv("DOCFIND_AI_MODEL", "first")
	t.Setenv("DOCFIND_AI_MODEL_2", "second")
	t.Setenv("DOCFIND_AI_URL_3", "http://other.invalid/v1")
	t.Setenv("DOCFIND_AI_KEY_3", "other-key")
	t.Setenv("DOCFIND_AI_MODEL_3", "third")

	chain := ChainFromEnv()
	if len(chain) != 3 {
		t.Fatalf("expected three entries, got %d: %+v", len(chain), chain)
	}
	if chain[1].Key != "primary-key" || chain[1].BaseURL != "http://primary.invalid/v1" {
		t.Errorf("entry 2 did not inherit the primary key/url: %+v", chain[1])
	}
	if chain[1].Model != "second" {
		t.Errorf("entry 2 model is %q", chain[1].Model)
	}
	if chain[2].Key != "other-key" || chain[2].BaseURL != "http://other.invalid/v1" {
		t.Errorf("entry 3 should keep its own key/url: %+v", chain[2])
	}
}

// A chain that tries the same dead model twice wastes the reader's time twice.
func TestChainDropsDuplicatesAndUnusableEntries(t *testing.T) {
	t.Setenv("DOCFIND_AI_PROVIDER", ProviderOpenAI)
	t.Setenv("DOCFIND_AI_URL", "http://primary.invalid/v1")
	t.Setenv("DOCFIND_AI_KEY", "k")
	t.Setenv("DOCFIND_AI_MODEL", "m")
	// Identical to the primary once inheritance is applied.
	t.Setenv("DOCFIND_AI_MODEL_2", "m")
	// Gemini with no key is unusable and must not reach the chain.
	t.Setenv("DOCFIND_AI_PROVIDER_3", ProviderGemini)
	t.Setenv("DOCFIND_AI_KEY_3", "")
	t.Setenv("DOCFIND_AI_MODEL_3", "g")
	t.Setenv("DOCFIND_AI_URL_3", "")

	chain := ChainFromEnv()
	for i, c := range chain {
		if !c.Enabled() {
			t.Errorf("entry %d is unusable: %+v", i, c)
		}
	}
	seen := map[Config]int{}
	for _, c := range chain {
		seen[c]++
		if seen[c] > 1 {
			t.Errorf("duplicate entry in the chain: %+v", c)
		}
	}
}

// The description sits beside a claim about where company data goes, so it
// must name every model that could answer -- not just the first.
//
// The bug this pins: naming only the head told the reader their findings went
// to gpt-4.1-mini while the answer on screen had come from the local
// fallback, because the hosted entry was out of credit.
func TestDescribeChainNamesEveryEntry(t *testing.T) {
	three := []Config{cfg("http://a", "m1"), cfg("http://b", "m2"), cfg("http://c", "m3")}
	got := DescribeChain(three)
	for _, want := range []string{"m1", "m2", "m3"} {
		if !strings.Contains(got, want) {
			t.Errorf("DescribeChain omitted %q: %q", want, got)
		}
	}
	if DescribeChain(nil) != "" {
		t.Error("an empty chain described itself as something")
	}
}

// A local model must not be described by the wire protocol it happens to
// speak. "openai / qwen2.5:3b" reads as though the data went to OpenAI when
// nothing left the machine at all.
func TestLocalModelIsNotDescribedAsItsWireFormat(t *testing.T) {
	local := Config{Provider: ProviderOpenAI, Model: "qwen2.5:3b",
		BaseURL: "http://127.0.0.1:11434/v1"}
	got := local.Describe()
	if strings.Contains(strings.ToLower(got), "openai") {
		t.Errorf("a local model described itself as openai: %q", got)
	}
	if !strings.Contains(got, "qwen2.5:3b") {
		t.Errorf("the model name is missing: %q", got)
	}
	if !strings.Contains(got, "محلی") {
		t.Errorf("the description does not say it is local: %q", got)
	}
}

// "No data leaves the building" is only true when every entry is local.
func TestAllLocalIsTrueOnlyWhenNothingIsHosted(t *testing.T) {
	localA := Config{Provider: ProviderOpenAI, Model: "a", BaseURL: "http://127.0.0.1:11434/v1"}
	localB := Config{Provider: ProviderOpenAI, Model: "b", BaseURL: "http://localhost:11434/v1"}
	hosted := Config{Provider: ProviderOpenAI, Key: "k", Model: "c", BaseURL: "https://api.openai.com/v1"}

	if !AllLocal([]Config{localA, localB}) {
		t.Error("an all-local chain was not reported as local")
	}
	if AllLocal([]Config{hosted, localA}) {
		t.Error("a chain containing a hosted entry claimed nothing leaves the building")
	}
	if AllLocal([]Config{localA, hosted}) {
		t.Error("a hosted fallback was overlooked")
	}
	if AllLocal(nil) {
		t.Error("an empty chain claimed to be local")
	}
}

// The failure that motivated the chain: a valid key with no credit left,
// answering 429 on every model in the account.
func TestChainFallsPastAnExhaustedAccount(t *testing.T) {
	broke := failServer(t, http.StatusTooManyRequests,
		`{"error":{"message":"You have no credits remaining.","code":"credit_balance_exhausted"}}`)
	defer broke.Close()
	good := okServer(t, "پیشنهاد")
	defer good.Close()

	chain := []Config{cfg(broke.URL, "gpt-4.1-mini"), cfg(good.URL, "llama-3.3-70b")}
	answer, used, err := CompleteChain(context.Background(), chain, "", "q")
	if err != nil {
		t.Fatalf("chain gave up despite a working fallback: %v", err)
	}
	if answer != "پیشنهاد" {
		t.Errorf("answer was %q", answer)
	}
	// The reader is told which service actually produced the text, so the
	// attribution on the page is not a lie after a fallback.
	if used.Model != "llama-3.3-70b" {
		t.Errorf("reported model %q, want the one that answered", used.Model)
	}
}

func TestChainKeepsTryingPastTransportFailures(t *testing.T) {
	good := okServer(t, "ok")
	defer good.Close()

	// A port nothing is listening on stands in for the blocked address.
	chain := []Config{cfg("http://127.0.0.1:9", "dead"), cfg(good.URL, "live")}
	answer, used, err := CompleteChain(context.Background(), chain, "", "q")
	if err != nil {
		t.Fatalf("a dead first entry ended the chain: %v", err)
	}
	if answer != "ok" || used.Model != "live" {
		t.Errorf("got %q from %q", answer, used.Model)
	}
}

// An empty answer is a failure worth passing on, not a result.
func TestChainTreatsAnEmptyAnswerAsFailure(t *testing.T) {
	empty := okServer(t, "   ")
	defer empty.Close()
	good := okServer(t, "real")
	defer good.Close()

	answer, used, err := CompleteChain(context.Background(),
		[]Config{cfg(empty.URL, "mute"), cfg(good.URL, "talks")}, "", "q")
	if err != nil {
		t.Fatalf("chain failed: %v", err)
	}
	if answer != "real" || used.Model != "talks" {
		t.Errorf("got %q from %q", answer, used.Model)
	}
}

// A prompt refused on content grounds fails the same way everywhere, so the
// chain must stop rather than spend every quota in the list proving it.
func TestChainStopsOnANonRetryableRefusal(t *testing.T) {
	var secondCalls int
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`))
	}))
	defer blocked.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Write([]byte(`{"choices":[{"message":{"content":"unused"}}]}`))
	}))
	defer second.Close()

	first := Config{Provider: ProviderGemini, Key: "k", Model: "g", BaseURL: blocked.URL}
	_, _, err := CompleteChain(context.Background(),
		[]Config{first, cfg(second.URL, "next")}, "", "q")
	if err == nil {
		t.Fatal("a blocked prompt was reported as success")
	}
	if !strings.Contains(err.Error(), "SAFETY") {
		t.Errorf("the reason was lost: %v", err)
	}
	if secondCalls != 0 {
		t.Errorf("the fallback was called %d times for a refusal that would repeat", secondCalls)
	}
}

// When everything fails, every reason is reported. Showing only the last one
// hid the useful message: an exhausted key first and a typo second told the
// reader about the typo and never about the credit.
func TestChainReportsEveryFailure(t *testing.T) {
	a := failServer(t, http.StatusTooManyRequests,
		`{"error":{"message":"no credits remaining","code":"credit_balance_exhausted"}}`)
	defer a.Close()
	b := failServer(t, http.StatusNotFound,
		`{"error":{"message":"model not found","code":"model_not_found"}}`)
	defer b.Close()

	_, _, err := CompleteChain(context.Background(),
		[]Config{cfg(a.URL, "first"), cfg(b.URL, "second")}, "", "q")
	if err == nil {
		t.Fatal("an all-failed chain reported success")
	}
	for _, want := range []string{"no credits remaining", "model not found", "first", "second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the combined error is missing %q: %v", want, err)
		}
	}
}

func TestCompleteChainRefusesAnEmptyChain(t *testing.T) {
	if _, _, err := CompleteChain(context.Background(), nil, "", "q"); err == nil {
		t.Fatal("an empty chain reported success")
	}
}

func TestChainErrorSeparatesReaderAndLogText(t *testing.T) {
	down := failServer(t, http.StatusTooManyRequests,
		`{"error":{"message":"no credits remaining","code":"credit_balance_exhausted"}}`)
	defer down.Close()

	_, _, err := CompleteChain(context.Background(),
		[]Config{cfg(down.URL, "hosted"), cfg("http://127.0.0.1:9", "local")},
		"", "q")
	if err == nil {
		t.Fatal("an all-failed chain reported success")
	}
	ce, ok := err.(*ChainError)
	if !ok {
		t.Fatalf("expected a *ChainError, got %T", err)
	}
	if strings.Contains(ce.Error(), "dial tcp") {
		t.Errorf("the reader's message carries a dial error: %q", ce.Error())
	}
	if !strings.Contains(ce.Verbose(), "dial tcp") {
		t.Errorf("the log lost the dial detail: %q", ce.Verbose())
	}
	// Both forms must still name every attempt.
	for _, s := range []string{ce.Error(), ce.Verbose()} {
		if !strings.Contains(s, "hosted") || !strings.Contains(s, "local") {
			t.Errorf("an attempt is missing from %q", s)
		}
	}
}

package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Nothing leaves the building until somebody configures a key. This is the
// whole privacy stance of the feature, so it is asserted rather than assumed.
func TestDisabledUntilConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"no key at all", Config{Provider: ProviderGemini}, false},
		{"gemini with a key", Config{Provider: ProviderGemini, Key: "k"}, true},
		{"openai with no url", Config{Provider: ProviderOpenAI, Key: "k"}, false},
		{"openai with a url", Config{Provider: ProviderOpenAI, Key: "k", BaseURL: "http://x"}, true},
		// A local relay holds the provider credentials itself and documents
		// the client key as unused, so a URL alone must be enough.
		{"local relay, no key", Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:19280/v1"}, true},
		{"unknown provider", Config{Provider: "wat", Key: "k"}, false},
	}
	for _, c := range cases {
		if got := c.cfg.Enabled(); got != c.want {
			t.Errorf("%s: Enabled() = %v, want %v", c.name, got, c.want)
		}
	}
}

// The key must never be shown to the reader, and Describe is rendered on the
// page.
func TestDescribeNeverCarriesTheKey(t *testing.T) {
	cfg := Config{Provider: ProviderGemini, Key: "super-secret-key", Model: "m"}
	d := cfg.Describe()
	if strings.Contains(d, "super-secret-key") {
		t.Fatalf("Describe() leaked the key: %q", d)
	}
	if !strings.Contains(d, "m") {
		t.Errorf("Describe() = %q, expected it to name the model", d)
	}
	if (Config{}).Describe() != "" {
		t.Error("an unconfigured client described itself as something")
	}
}

// A model on this machine is a different proposition from a hosted one, and
// the whole timeout policy turns on telling them apart.
func TestIsLocalRecognisesLoopback(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:11434/v1", true},
		{"http://localhost:11434/v1", true},
		{"http://[::1]:11434/v1", true},
		// The whole 127/8 range is loopback, not just .0.1.
		{"http://127.5.9.3:11434/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://api.groq.com/openai/v1", false},
		// The server's own LAN address is NOT loopback: reaching it goes over
		// the network and deserves the short timeout.
		{"http://192.168.77.4:11434/v1", false},
		{"", false},
	}
	for _, c := range cases {
		got := Config{Provider: ProviderOpenAI, BaseURL: c.url}.IsLocal()
		if got != c.want {
			t.Errorf("IsLocal(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestTimeoutIsLongerForALocalModel(t *testing.T) {
	local := Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:11434/v1"}
	hosted := Config{Provider: ProviderOpenAI, Key: "k", BaseURL: "https://api.openai.com/v1"}

	if local.Timeout() <= hosted.Timeout() {
		t.Errorf("local timeout %v is not longer than hosted %v; CPU inference "+
			"would be cancelled just before it answered", local.Timeout(), hosted.Timeout())
	}
	if hosted.Timeout() != askTimeout {
		t.Errorf("hosted timeout is %v, want %v", hosted.Timeout(), askTimeout)
	}
	if local.Timeout() != localTimeout {
		t.Errorf("local timeout is %v, want %v", local.Timeout(), localTimeout)
	}

	// The override wins over both, for the case nobody anticipated.
	t.Setenv("DOCFIND_AI_TIMEOUT", "90")
	if got := local.Timeout(); got != 90*time.Second {
		t.Errorf("with the override set, local timeout is %v, want 90s", got)
	}
	if got := hosted.Timeout(); got != 90*time.Second {
		t.Errorf("with the override set, hosted timeout is %v, want 90s", got)
	}

	// Nonsense is ignored rather than producing a zero timeout, which would
	// cancel every request immediately.
	for _, bad := range []string{"0", "-5", "abc", " "} {
		t.Setenv("DOCFIND_AI_TIMEOUT", bad)
		if got := hosted.Timeout(); got != askTimeout {
			t.Errorf("DOCFIND_AI_TIMEOUT=%q gave %v, want the default %v", bad, got, askTimeout)
		}
	}
}

func TestCompleteReturnsTheModelsAnswer(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// The whole body, not the first 4 KB: a single Read can return only the
		// opening of a long prompt, and the assertion would then fail on a
		// truncated read rather than on anything the code did wrong.
		all, _ := io.ReadAll(r.Body)
		gotBody = string(all)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"۱) تفسیر: ...\n۲) اقدام‌ها: ..."}}]}`))
	}))
	defer srv.Close()

	cfg := Config{Provider: ProviderOpenAI, Key: "tok", Model: "m", BaseURL: srv.URL}
	out, err := Complete(context.Background(), cfg, "", "بیشترین بار ۷.۷ برابر میانه است")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(out, "تفسیر") {
		t.Errorf("answer came back as %q", out)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization header was %q", gotAuth)
	}
	if !strings.Contains(gotBody, "برابر میانه") {
		t.Error("the prompt did not reach the request body")
	}
}

// With no key there must be no Authorization header at all. An empty bearer
// token is rejected by some relays that would have been happy with no header.
func TestKeylessRelaySendsNoAuthorizationHeader(t *testing.T) {
	seen := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seen = r.Header["Authorization"]
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	out, err := Complete(context.Background(),
		Config{Provider: ProviderOpenAI, Model: "m", BaseURL: srv.URL},
		"", "q")
	if err != nil {
		t.Fatalf("Complete against a keyless relay: %v", err)
	}
	if out != "ok" {
		t.Errorf("got %q", out)
	}
	if seen {
		t.Error("an Authorization header was sent even though no key is configured")
	}
}

func TestGeminiShapeIsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("x-goog-api-key"); k != "gk" {
			t.Errorf("key header was %q", k)
		}
		if strings.Contains(r.URL.RawQuery, "gk") {
			t.Error("the key was put in the query string, where proxy logs keep it")
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"پیشنهاد"}]}}]}`))
	}))
	defer srv.Close()

	out, err := Complete(context.Background(),
		Config{Provider: ProviderGemini, Key: "gk", Model: "m", BaseURL: srv.URL},
		"", "q")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "پیشنهاد" {
		t.Errorf("got %q", out)
	}
}

// A provider's failure must be reported without republishing the key, since
// this text reaches the page and the server log.
func TestProviderErrorDoesNotLeakTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key: sekrit-key-42"}`))
	}))
	defer srv.Close()

	_, err := Complete(context.Background(),
		Config{Provider: ProviderOpenAI, Key: "sekrit-key-42", Model: "m", BaseURL: srv.URL},
		"", "q")
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if strings.Contains(err.Error(), "sekrit-key-42") {
		t.Errorf("the error republished the key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error hides the status code, which is what identifies it: %v", err)
	}
}

// A real failure seen in production, and the reason providerError exists.
//
// OpenAI pretty-prints its error bodies, so taking the first line handed the
// reader «HTTP 429): {» -- technically true and completely useless. The
// sentence underneath said "You have no credits remaining", which tells them
// exactly what to do, and the code tells quota exhaustion apart from ordinary
// rate limiting even though both arrive as 429.
func TestPrettyPrintedProviderErrorStaysReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("{\n    \"error\": {\n" +
			"        \"message\": \"You have no credits remaining. Add credits to continue.\",\n" +
			"        \"type\": \"insufficient_quota\",\n" +
			"        \"code\": \"credit_balance_exhausted\"\n    }\n}"))
	}))
	defer srv.Close()

	_, err := Complete(context.Background(),
		Config{Provider: ProviderOpenAI, Key: "k", Model: "m", BaseURL: srv.URL},
		"", "q")
	if err == nil {
		t.Fatal("a 429 was reported as success")
	}
	got := err.Error()
	if !strings.Contains(got, "no credits remaining") {
		t.Errorf("the actionable sentence was dropped: %v", got)
	}
	if !strings.Contains(got, "credit_balance_exhausted") {
		t.Errorf("the code separating quota from rate limiting was dropped: %v", got)
	}
	if strings.HasSuffix(strings.TrimSpace(got), "{") {
		t.Error("the error still ends at the opening brace")
	}
}

func TestBlockedPromptIsReportedNotSilentlyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`))
	}))
	defer srv.Close()

	_, err := Complete(context.Background(),
		Config{Provider: ProviderGemini, Key: "k", Model: "m", BaseURL: srv.URL},
		"", "q")
	if err == nil || !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("a blocked prompt came back as %v, want the reason reported", err)
	}
}

func TestEmptyAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"   "}}]}`))
	}))
	defer srv.Close()

	if _, err := Complete(context.Background(),
		Config{Provider: ProviderOpenAI, Key: "k", Model: "m", BaseURL: srv.URL},
		"", "q"); err == nil {
		t.Fatal("a blank answer was returned as if it were an answer")
	}
}

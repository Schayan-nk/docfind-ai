package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// One real call to the configured chain, with a real key.
//
// Skipped unless DOCFIND_AI_LIVE_TEST is set, so it never runs in an ordinary
// `go test ./...` and never needs a key to be present. It exists because every
// other test in this package checks the shape of a request that nothing ever
// sent: the model name, the token parameter and the temperature rule are all
// asserted against bytes, and bytes can be correct while the account, the key
// or the endpoint is not.
//
// Run it as:
//
//	DOCFIND_AI_LIVE_TEST=1 DOCFIND_AI_PROVIDER=openai \
//	DOCFIND_AI_URL=https://api.openai.com/v1 DOCFIND_AI_KEY=sk-... \
//	go test ./llm/ -run Live -v
//
// The key is read from the environment and never written anywhere.
func TestLiveCallToTheConfiguredModel(t *testing.T) {
	if os.Getenv("DOCFIND_AI_LIVE_TEST") == "" {
		t.Skip("set DOCFIND_AI_LIVE_TEST=1 to make one real, billable request")
	}

	chain := ChainFromEnv()
	if !ChainEnabled(chain) {
		t.Fatal("nothing is configured; set DOCFIND_AI_PROVIDER, DOCFIND_AI_URL and " +
			"DOCFIND_AI_KEY")
	}
	t.Logf("chain: %s", DescribeChain(chain))
	for i, c := range chain {
		t.Logf("  [%d] provider=%q model=%q url=%q key=%s local=%v",
			i, c.Provider, c.Model, c.BaseURL, maskKey(c.Key), c.IsLocal())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	answer, used, err := CompleteChain(ctx, chain,
		"پاسخ را فقط با یک واژهٔ فارسی بده. "+PersianStyle,
		"پایتخت ایران کدام شهر است؟")
	took := time.Since(start)
	if err != nil {
		t.Fatalf("the whole chain failed after %s:\n%v", took.Round(time.Millisecond), err)
	}
	t.Logf("answered by %s in %s: %q", used.Describe(), took.Round(time.Millisecond), answer)
	if !strings.Contains(answer, "تهران") {
		t.Errorf("the answer %q does not name the city", answer)
	}
}

// maskKey shows enough of a key to tell two apart and not enough to use one.
func maskKey(k string) string {
	if k == "" {
		return "(unset)"
	}
	if len(k) <= 12 {
		return "(set, short)"
	}
	return k[:7] + "…" + k[len(k)-4:] + " (" + itoa(len(k)) + " chars)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

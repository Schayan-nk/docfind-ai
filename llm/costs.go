package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// What the ChatGPT account has left.
//
// OpenAI does not tell an API key its balance. The billing endpoints that once
// did (dashboard/billing/credit_grants and its siblings) answer only a browser
// session now, never a key. What a program CAN read is:
//
//   - the organisation's spending, day by day, from the Costs API -- with an
//     ADMIN key (sk-admin-...), which only an organisation owner can make, and
//     which reads the bill but cannot call a model;
//   - whether a real request is refused for want of credit: a 429 whose code
//     says the quota is spent, which no amount of waiting will clear.
//
// So the balance is worked out, not read: the figure the billing page showed
// on a known day, less what the Costs API says was spent since. That is the
// honest version, and whatever shows it should say it is an estimate.

// CostsURL is the Costs API. A variable so tests can point it elsewhere.
var CostsURL = "https://api.openai.com/v1/organization/costs"

// OrgCosts is what the organisation spent from since until now, in US
// dollars, by the Costs API's daily buckets. adminKey is an Admin API key.
func OrgCosts(ctx context.Context, adminKey string, since time.Time) (float64, error) {
	if strings.TrimSpace(adminKey) == "" {
		return 0, errors.New("کلید Admin تنظیم نشده است")
	}
	total, page := 0.0, ""
	// A bounded walk: 180 daily buckets a page, and ten pages is five years.
	for i := 0; i < 10; i++ {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(since.Unix(), 10))
		q.Set("bucket_width", "1d")
		q.Set("limit", "180")
		if page != "" {
			q.Set("page", page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, CostsURL+"?"+q.Encode(), nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		resp, err := httpClient.Do(req)
		if err != nil {
			return 0, errors.New(redactKey(scrub(err, adminKey).Error(), adminKey))
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("گزارش هزینهٔ OpenAI خطا داد (HTTP %d): %s", resp.StatusCode, redactKey(providerError(body), adminKey))
		}
		var out struct {
			Data []struct {
				Results []struct {
					Amount struct {
						Value    float64 `json:"value"`
						Currency string  `json:"currency"`
					} `json:"amount"`
				} `json:"results"`
			} `json:"data"`
			HasMore  bool   `json:"has_more"`
			NextPage string `json:"next_page"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return 0, errors.New("گزارش هزینهٔ OpenAI خوانا نبود")
		}
		for _, b := range out.Data {
			for _, r := range b.Results {
				if r.Amount.Currency == "" || strings.EqualFold(r.Amount.Currency, "usd") {
					total += r.Amount.Value
				}
			}
		}
		if !out.HasMore || out.NextPage == "" {
			return total, nil
		}
		page = out.NextPage
	}
	return total, nil
}

// ProbeCredit sends the smallest request the provider takes -- the breakers'
// probe -- and returns its failure, nil when the key was accepted with credit
// behind it. A few tokens; meant to run an hour apart, not on every page.
func ProbeCredit(ctx context.Context, cfg Config) error { return probe(cfg)(ctx) }

// QuotaSpent reports whether err is a refusal for want of credit: the quota
// or the prepaid balance is spent, or the hard billing limit reached. A plain
// rate limit is also a 429, and is not this -- it clears by itself.
func QuotaSpent(err error) bool {
	var pe *ProviderError
	if !errors.As(err, &pe) || (pe.Status != http.StatusTooManyRequests && pe.Status != http.StatusPaymentRequired) {
		return false
	}
	m := strings.ToLower(pe.Msg)
	for _, w := range []string{"insufficient_quota", "credit_balance", "billing_hard_limit", "exceeded your current quota",
		"no credits remaining"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return pe.Status == http.StatusPaymentRequired
}

// KeyRefused reports whether err says the key itself is not accepted.
func KeyRefused(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && (pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden)
}

// IsOpenAIPlatform reports whether cfg talks to OpenAI itself -- not a local
// relay or another provider that speaks the same API, whose balance is
// nothing to do with OpenAI's.
func IsOpenAIPlatform(cfg Config) bool {
	return cfg.Provider == ProviderOpenAI && strings.Contains(strings.ToLower(cfg.BaseURL), "api.openai.com")
}

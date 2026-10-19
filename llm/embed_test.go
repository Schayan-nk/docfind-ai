package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The vectors come back in the order the texts went, whatever order the
// endpoint lists them in, each scaled to length one; the key goes as a
// bearer token; a refusal is a ProviderError; nothing is sent when
// embeddings are not configured.
func TestEmbed(t *testing.T) {
	var gotAuth string
	var gotBody struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		if gotBody.Input[0] == "fail" {
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"data":[{"index":1,"embedding":[0,2]},{"index":0,"embedding":[3,4]}]}`))
	}))
	defer srv.Close()
	cfg := Config{Provider: ProviderOpenAI, BaseURL: srv.URL + "/v1", Key: "sk-test"}

	vecs, err := Embed(context.Background(), cfg, "nomic-embed-text", []string{"پیش‌فاکتور", "proforma"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" || gotBody.Model != "nomic-embed-text" || len(gotBody.Input) != 2 {
		t.Errorf("request: auth %q body %+v", gotAuth, gotBody)
	}
	if len(vecs) != 2 || math.Abs(float64(vecs[0][0])-0.6) > 1e-6 || math.Abs(float64(vecs[0][1])-0.8) > 1e-6 || vecs[1][1] != 1 {
		t.Errorf("vectors %v", vecs)
	}
	if c := Cosine(vecs[0], vecs[1]); math.Abs(c-0.8) > 1e-6 {
		t.Errorf("cosine %v, want 0.8", c)
	}

	_, err = Embed(context.Background(), cfg, "nomic-embed-text", []string{"fail"})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusNotFound {
		t.Errorf("a refusal came back as %v", err)
	}
	if _, err := Embed(context.Background(), cfg, "", []string{"x"}); !errors.Is(err, ErrNoEmbeddings) {
		t.Errorf("no model: %v", err)
	}
	if _, err := Embed(context.Background(), Config{Provider: ProviderGemini, Key: "k"}, "m", []string{"x"}); !errors.Is(err, ErrNoEmbeddings) {
		t.Errorf("not an OpenAI-shaped endpoint: %v", err)
	}
}

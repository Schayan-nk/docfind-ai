package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Embeddings: a text turned into a list of numbers such that texts about the
// same thing come out close together, whatever words they use -- the way to
// find the messages or documents like the one being read.
//
// Off unless DOCFIND_EMBED_MODEL names an embedding model -- a chat model
// cannot do this -- because it sends the text being embedded to the
// configured endpoint. On a local relay (Ollama on this machine) nothing
// leaves the building; on a hosted one the text does, and that is a choice
// an administrator makes by setting the variable, not one this makes for
// them. The endpoint, key and network guard are the chain's own (Config,
// httpClient): the OpenAI shape, POST /embeddings, which Ollama and the other
// local relays serve too.

// embedLimit bounds one reply: a batch of vectors is large, but not this.
const embedLimit = 32 << 20

// EmbedModel is the embedding model to use, or "" when embeddings are off.
func EmbedModel() string { return strings.TrimSpace(os.Getenv("DOCFIND_EMBED_MODEL")) }

// ErrNoEmbeddings is returned when the endpoint cannot embed: not configured,
// or not an OpenAI-shaped one.
var ErrNoEmbeddings = errors.New("llm: no embedding endpoint configured")

// EmbedConfig picks, from the provider chain, the endpoint to embed with:
// an OpenAI-shaped one, a local one first so the text stays in the building
// when it can.
func EmbedConfig(chain []Config) (Config, bool) {
	var hosted *Config
	for i := range chain {
		c := chain[i]
		if c.Provider != ProviderOpenAI || c.BaseURL == "" {
			continue
		}
		if c.IsLocal() {
			return c, true
		}
		if hosted == nil {
			hosted = &chain[i]
		}
	}
	if hosted != nil {
		return *hosted, true
	}
	return Config{}, false
}

// Embed returns one vector per text, in order, each scaled to length one so
// that a dot product is their cosine similarity.
func Embed(ctx context.Context, cfg Config, model string, texts []string) ([][]float32, error) {
	if model == "" || cfg.Provider != ProviderOpenAI || cfg.BaseURL == "" {
		return nil, ErrNoEmbeddings
	}
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Key)
	}
	req.Header.Set("User-Agent", "docfind-ai/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, &ProviderError{Transport: true, Local: cfg.IsLocal(), Provider: cfg.Provider, Model: model,
			Msg: scrub(err, cfg.Key).Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, embedLimit))
	if err != nil {
		return nil, &ProviderError{Transport: true, Local: cfg.IsLocal(), Provider: cfg.Provider, Model: model,
			Msg: scrub(err, cfg.Key).Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Status: resp.StatusCode, Provider: cfg.Provider, Model: model,
			Msg: redactKey(providerError(raw), cfg.Key)}
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("llm: the embedding reply could not be read")
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) && len(d.Embedding) > 0 {
			vecs[d.Index] = unit(d.Embedding)
		}
	}
	for i, v := range vecs {
		if v == nil {
			return nil, errors.New("llm: the embedding reply is missing text " + strconv.Itoa(i))
		}
	}
	return vecs, nil
}

func unit(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

// Cosine is the similarity of two vectors Embed returned: 1 the same, 0
// unrelated.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

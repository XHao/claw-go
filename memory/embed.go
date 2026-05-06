package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

const defaultEmbedTimeout = 5 * time.Second

// Embedder calls a local Ollama-compatible embedding endpoint.
type Embedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewEmbedder returns an Embedder pointing at baseURL (e.g. "http://localhost:11435")
// using the given model name.
func NewEmbedder(baseURL, model string) *Embedder {
	return &Embedder{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: defaultEmbedTimeout},
	}
}

// Embed returns the embedding vector for text. Returns an error if the
// endpoint is unreachable or returns a non-200 status.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]string{
		"model":  e.model,
		"prompt": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d", resp.StatusCode)
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty vector returned")
	}
	return result.Embedding, nil
}

// CosineSim returns the cosine similarity in [-1, 1] between two vectors.
// Returns 0 if either vector has zero magnitude.
func CosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// turnText builds the text used for embedding a TurnSummary.
// Combines user message, LLM summary (if present), and tool summaries.
func turnText(t TurnSummary) string {
	var buf bytes.Buffer
	buf.WriteString(t.User)
	if t.LLMSummary != "" {
		buf.WriteString(" ")
		buf.WriteString(t.LLMSummary)
	} else if t.Reply != "" {
		buf.WriteString(" ")
		buf.WriteString(t.Reply)
	}
	for _, a := range t.Actions {
		if a.Summary != "" {
			buf.WriteString(" ")
			buf.WriteString(a.Summary)
		}
	}
	return buf.String()
}

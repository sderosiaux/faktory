package faktory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Embedder struct {
	baseURL      string
	apiKey       string
	model        string
	dimension    int
	httpClient   *http.Client
	customClient bool
}

// NewEmbedder creates an Embedder. If httpClient is nil, a default client with
// 30s timeout is used and transient-error retry is enabled.
func NewEmbedder(baseURL, apiKey, model string, dimension int, httpClient *http.Client) *Embedder {
	custom := httpClient != nil
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Embedder{
		baseURL:      baseURL,
		apiKey:       apiKey,
		model:        model,
		dimension:    dimension,
		httpClient:   httpClient,
		customClient: custom,
	}
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for a single text input.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedding returned no vectors")
	}
	return vecs[0], nil
}

// EmbedBatch returns embedding vectors for multiple texts.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	req := embeddingRequest{
		Model: e.model,
		Input: texts,
	}
	if e.dimension > 0 {
		req.Dimensions = e.dimension
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	vecs, statusCode, retryAfterHeader, err := e.sendEmbedHTTP(ctx, body)
	if err == nil {
		return vecs, nil
	}

	// No retry for custom clients or non-retryable status codes.
	if e.customClient || !isRetryable(statusCode) {
		return nil, err
	}

	delay, ok := retryDelay(statusCode, retryAfterHeader)
	if !ok {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(delay):
	}

	vecs, _, _, retryErr := e.sendEmbedHTTP(ctx, body)
	if retryErr != nil {
		return nil, retryErr
	}
	return vecs, nil
}

// sendEmbedHTTP performs the raw embedding HTTP POST.
func (e *Embedder) sendEmbedHTTP(ctx context.Context, body []byte) ([][]float32, int, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, "", fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("read embedding response: %w", err)
	}

	if resp.StatusCode != 200 {
		ra := resp.Header.Get("Retry-After")
		return nil, resp.StatusCode, ra, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, 200, "", fmt.Errorf("decode embedding response: %w", err)
	}

	vecs := make([][]float32, len(embResp.Data))
	for i, d := range embResp.Data {
		vecs[i] = d.Embedding
	}
	return vecs, 200, "", nil
}

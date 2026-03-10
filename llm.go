package faktory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type LLM struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	// useJSONObject is set to true if json_schema mode fails (fallback for Ollama/Groq)
	useJSONObject bool
	temperature   float64
	// customClient is true when the caller provided their own http.Client;
	// retry logic is skipped in that case.
	customClient bool
}

// NewLLM creates an LLM client. If httpClient is nil, a default client with
// 30s timeout is used and transient-error retry is enabled.
func NewLLM(baseURL, apiKey, model string, httpClient *http.Client) *LLM {
	custom := httpClient != nil
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &LLM{
		baseURL:      baseURL,
		apiKey:       apiKey,
		model:        model,
		httpClient:   httpClient,
		customClient: custom,
	}
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete sends a chat completion request, parses the JSON response into result,
// and returns the total tokens consumed across all attempts.
func (l *LLM) Complete(ctx context.Context, system, user string, schemaName string, schema json.RawMessage, result any) (int, error) {
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	var rf *responseFormat
	if l.useJSONObject {
		rf = &responseFormat{Type: "json_object"}
	} else {
		rf = &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaSpec{
				Name:   schemaName,
				Strict: true,
				Schema: schema,
			},
		}
	}

	totalTokens := 0
	httpRetried := false

	raw, tokens, retried, err := l.doRequest(ctx, messages, rf)
	totalTokens += tokens
	if retried {
		httpRetried = true
	}
	if err != nil {
		// If json_schema mode fails with 400, fall back to json_object
		if !l.useJSONObject {
			l.useJSONObject = true
			rf = &responseFormat{Type: "json_object"}
			raw, tokens, retried, err = l.doRequest(ctx, messages, rf)
			totalTokens += tokens
			if retried {
				httpRetried = true
			}
			if err != nil {
				return totalTokens, err
			}
		} else {
			return totalTokens, err
		}
	}

	if err := json.Unmarshal([]byte(raw), result); err != nil {
		// Skip parse-failure retry if HTTP already retried (max 2 HTTP requests per Complete call)
		if httpRetried {
			return totalTokens, fmt.Errorf("parse LLM response: %w\nraw: %s", err, raw)
		}
		raw, tokens, _, retryErr := l.doRequest(ctx, messages, rf)
		totalTokens += tokens
		if retryErr != nil {
			return totalTokens, fmt.Errorf("retry after parse error %w: %w", err, retryErr)
		}
		if err := json.Unmarshal([]byte(raw), result); err != nil {
			return totalTokens, fmt.Errorf("parse LLM response: %w\nraw: %s", err, raw)
		}
	}

	return totalTokens, nil
}

// CompleteWithCorrection sends a correction request: re-sends the original conversation
// plus the previous LLM response and a correction prompt. Returns accumulated tokens.
func (l *LLM) CompleteWithCorrection(ctx context.Context, system, user, previousResponse, correction string, schemaName string, schema json.RawMessage, result any) (int, error) {
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
		{Role: "assistant", Content: previousResponse},
		{Role: "user", Content: correction},
	}

	var rf *responseFormat
	if l.useJSONObject {
		rf = &responseFormat{Type: "json_object"}
	} else {
		rf = &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaSpec{
				Name:   schemaName,
				Strict: true,
				Schema: schema,
			},
		}
	}

	raw, tokens, _, err := l.doRequest(ctx, messages, rf)
	if err != nil {
		return tokens, err
	}

	if err := json.Unmarshal([]byte(raw), result); err != nil {
		return tokens, fmt.Errorf("parse correction response: %w\nraw: %s", err, raw)
	}

	return tokens, nil
}

const maxRetryAfter = 10 * time.Second

// doRequest sends a single chat completion HTTP request. It returns
// (content, tokens, httpRetried, error). When the caller provided a custom
// HTTP client, retry is skipped entirely.
func (l *LLM) doRequest(ctx context.Context, messages []chatMessage, rf *responseFormat) (string, int, bool, error) {
	req := chatRequest{
		Model:          l.model,
		Messages:       messages,
		Temperature:    l.temperature,
		ResponseFormat: rf,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", 0, false, err
	}

	content, tokens, statusCode, retryAfterHeader, err := l.sendHTTP(ctx, body)
	if err == nil {
		return content, tokens, false, nil
	}

	// No retry for custom clients or non-retryable status codes.
	if l.customClient || !isRetryable(statusCode) {
		return "", tokens, false, err
	}

	delay, ok := retryDelay(statusCode, retryAfterHeader)
	if !ok {
		// Retry-After exceeds cap — fail immediately.
		return "", tokens, false, err
	}

	select {
	case <-ctx.Done():
		return "", tokens, false, ctx.Err()
	case <-time.After(delay):
	}

	content, retryTokens, _, _, retryErr := l.sendHTTP(ctx, body)
	tokens += retryTokens
	if retryErr != nil {
		return "", tokens, true, retryErr
	}
	return content, tokens, true, nil
}

// sendHTTP performs the raw HTTP POST and returns parsed content on 200.
// On non-200 it returns the status code and Retry-After header for the caller
// to decide on retry.
func (l *LLM) sendHTTP(ctx context.Context, body []byte) (string, int, int, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", l.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if l.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.apiKey)
	}

	resp, err := l.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("LLM request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, resp.StatusCode, "", fmt.Errorf("read LLM response: %w", err)
	}

	if resp.StatusCode != 200 {
		ra := resp.Header.Get("Retry-After")
		return "", 0, resp.StatusCode, ra, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", 0, 200, "", fmt.Errorf("decode LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", 0, 200, "", fmt.Errorf("LLM returned no choices")
	}

	return chatResp.Choices[0].Message.Content, chatResp.Usage.TotalTokens, 200, "", nil
}

// isRetryable returns true for HTTP status codes that warrant a single retry.
func isRetryable(statusCode int) bool {
	switch statusCode {
	case 429, 500, 502, 503:
		return true
	}
	return false
}

// retryDelay returns the delay before retrying and whether a retry is allowed.
// For 429, uses Retry-After header (capped at maxRetryAfter). For 5xx, 1s.
func retryDelay(statusCode int, retryAfterHeader string) (time.Duration, bool) {
	if statusCode == 429 {
		secs, err := strconv.Atoi(retryAfterHeader)
		if err != nil || secs <= 0 {
			secs = 1
		}
		d := time.Duration(secs) * time.Second
		if d > maxRetryAfter {
			return 0, false
		}
		return d, true
	}
	// 500/502/503
	return 1 * time.Second, true
}

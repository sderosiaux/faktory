package faktory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type LLM struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	// useJSONObject is set to true if json_schema mode fails (fallback for Ollama/Groq)
	useJSONObject bool
}

func NewLLM(baseURL, apiKey, model string) *LLM {
	return &LLM{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{},
	}
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat *responseFormat   `json:"response_format,omitempty"`
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
}

// Complete sends a chat completion request and returns the parsed JSON response.
// It tries json_schema mode first, falls back to json_object if that fails.
func (l *LLM) Complete(ctx context.Context, system, user string, schemaName string, schema json.RawMessage, result any) error {
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

	raw, err := l.doRequest(ctx, messages, rf)
	if err != nil {
		// If json_schema mode fails with 400, fall back to json_object
		if !l.useJSONObject {
			l.useJSONObject = true
			rf = &responseFormat{Type: "json_object"}
			raw, err = l.doRequest(ctx, messages, rf)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if err := json.Unmarshal([]byte(raw), result); err != nil {
		// Retry once on parse failure
		raw, retryErr := l.doRequest(ctx, messages, rf)
		if retryErr != nil {
			return fmt.Errorf("retry failed: %w (original parse error: %v)", retryErr, err)
		}
		if err := json.Unmarshal([]byte(raw), result); err != nil {
			return fmt.Errorf("parse LLM response: %w\nraw: %s", err, raw)
		}
	}

	return nil
}

func (l *LLM) doRequest(ctx context.Context, messages []chatMessage, rf *responseFormat) (string, error) {
	req := chatRequest{
		Model:          l.model,
		Messages:       messages,
		Temperature:    0,
		ResponseFormat: rf,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", l.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if l.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.apiKey)
	}

	resp, err := l.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read LLM response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("decode LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}

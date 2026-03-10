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
	temperature   float64
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

	raw, tokens, err := l.doRequest(ctx, messages, rf)
	totalTokens += tokens
	if err != nil {
		// If json_schema mode fails with 400, fall back to json_object
		if !l.useJSONObject {
			l.useJSONObject = true
			rf = &responseFormat{Type: "json_object"}
			raw, tokens, err = l.doRequest(ctx, messages, rf)
			totalTokens += tokens
			if err != nil {
				return totalTokens, err
			}
		} else {
			return totalTokens, err
		}
	}

	if err := json.Unmarshal([]byte(raw), result); err != nil {
		// Retry once on parse failure
		raw, tokens, retryErr := l.doRequest(ctx, messages, rf)
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

	raw, tokens, err := l.doRequest(ctx, messages, rf)
	if err != nil {
		return tokens, err
	}

	if err := json.Unmarshal([]byte(raw), result); err != nil {
		return tokens, fmt.Errorf("parse correction response: %w\nraw: %s", err, raw)
	}

	return tokens, nil
}

func (l *LLM) doRequest(ctx context.Context, messages []chatMessage, rf *responseFormat) (string, int, error) {
	req := chatRequest{
		Model:          l.model,
		Messages:       messages,
		Temperature:    l.temperature,
		ResponseFormat: rf,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", 0, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", l.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if l.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.apiKey)
	}

	resp, err := l.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("LLM request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read LLM response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", 0, fmt.Errorf("decode LLM response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", 0, fmt.Errorf("LLM returned no choices")
	}

	return chatResp.Choices[0].Message.Content, chatResp.Usage.TotalTokens, nil
}

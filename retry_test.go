package faktory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryDefaultTimeout(t *testing.T) {
	llm := NewLLM("http://localhost", "", "m", nil)
	if llm.httpClient.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", llm.httpClient.Timeout)
	}

	emb := NewEmbedder("http://localhost", "", "m", 128, nil)
	if emb.httpClient.Timeout != 30*time.Second {
		t.Errorf("embedder default timeout = %v, want 30s", emb.httpClient.Timeout)
	}
}

func TestRetryConfigHTTPTimeout(t *testing.T) {
	custom := 60 * time.Second
	cfg := Config{HTTPTimeout: custom}
	client := cfg.buildHTTPClient()
	if client.Timeout != custom {
		t.Errorf("timeout = %v, want %v", client.Timeout, custom)
	}
}

func TestRetryConfigHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 99 * time.Second}
	cfg := Config{HTTPClient: custom}
	client := cfg.buildHTTPClient()
	if client != custom {
		t.Error("expected custom HTTP client to be used as-is")
	}
}

func TestRetry429WithRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: `{"ok":true}`}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	raw, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if raw != `{"ok":true}` {
		t.Errorf("raw = %q", raw)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestRetry429RetryAfterCap(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	_, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for Retry-After > 10s")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry when Retry-After too large)", calls.Load())
	}
}

func TestRetry500(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`internal error`))
			return
		}
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: `{"ok":true}`}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	raw, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if raw != `{"ok":true}` {
		t.Errorf("raw = %q", raw)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestRetry400NoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	_, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 400)", calls.Load())
	}
}

func TestRetrySuccessNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: `{"ok":true}`}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	raw, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != `{"ok":true}` {
		t.Errorf("raw = %q", raw)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestRetrySecondAttemptSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`bad gateway`))
			return
		}
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: `{"result":"ok"}`}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	raw, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if raw != `{"result":"ok"}` {
		t.Errorf("raw = %q", raw)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestRetryHTTPRetryFlagPreventsParseRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`service unavailable`))
			return
		}
		// Return valid HTTP but invalid JSON parse target — triggers parse retry path
		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: `not-valid-json`}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	llm := NewLLM(srv.URL, "", "m", nil)
	var result map[string]any
	_, err := llm.Complete(context.Background(), "sys", "usr", "test", json.RawMessage(`{}`), &result)
	// Should fail with parse error — the parse-failure retry should be skipped
	// because HTTP already retried once (max 2 HTTP requests per Complete call).
	if err == nil {
		t.Fatal("expected parse error")
	}
	// 2 calls: 1st = 503, 2nd = success but bad JSON. Parse retry skipped.
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (HTTP retry happened, parse retry skipped)", calls.Load())
	}
}

func TestRetryEmbedder429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`rate limited`))
			return
		}
		resp := embeddingResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{{Embedding: []float32{0.1, 0.2}}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb := NewEmbedder(srv.URL, "", "m", 2, nil)
	vec, err := emb.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if len(vec) != 2 {
		t.Errorf("vec len = %d, want 2", len(vec))
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestRetryEmbedder400NoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer srv.Close()

	emb := NewEmbedder(srv.URL, "", "m", 2, nil)
	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestRetryCustomHTTPClientSkipsRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`error`))
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 5 * time.Second}
	llm := NewLLM(srv.URL, "", "m", custom)
	_, _, _, err := llm.doRequest(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 500 with custom client")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (custom client skips retry)", calls.Load())
	}
}

func TestRetryCustomHTTPClientEmbedderSkipsRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`error`))
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 5 * time.Second}
	emb := NewEmbedder(srv.URL, "", "m", 2, custom)
	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 500 with custom client")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (custom client skips retry)", calls.Load())
	}
}

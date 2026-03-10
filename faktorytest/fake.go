// Package faktorytest provides test doubles for the faktory package.
package faktorytest

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"sync"
)

// ReconcileAction mirrors the reconciliation action structure.
type ReconcileAction struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Event string `json:"event"`
}

// EntityResult mirrors an extracted entity.
type EntityResult struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RelationResult mirrors an extracted relation.
type RelationResult struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// FakeCompleter is a test double that returns pre-configured results based on
// the schema name.
type FakeCompleter struct {
	Facts     []string
	Reconcile []ReconcileAction
	Entities  []EntityResult
	Relations []RelationResult
	Tokens    int

	mu            sync.Mutex
	SystemPrompts map[string]string // schemaName -> system prompt received
}

// GetSystemPrompt returns the system prompt captured for a given schema name.
func (fc *FakeCompleter) GetSystemPrompt(schemaName string) string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.SystemPrompts == nil {
		return ""
	}
	return fc.SystemPrompts[schemaName]
}

// Complete returns canned results based on schemaName.
func (fc *FakeCompleter) Complete(_ context.Context, system, _ string, schemaName string, _ json.RawMessage, result any) (int, error) {
	fc.mu.Lock()
	if fc.SystemPrompts == nil {
		fc.SystemPrompts = make(map[string]string)
	}
	fc.SystemPrompts[schemaName] = system
	fc.mu.Unlock()

	var payload any
	switch schemaName {
	case "fact_extraction":
		payload = map[string]any{"facts": fc.Facts}
	case "reconcile_memory":
		payload = map[string]any{"memory": fc.Reconcile}
	case "entity_extraction":
		payload = map[string]any{
			"resolved_text": "",
			"entities":      fc.Entities,
			"relations":     fc.Relations,
		}
	case "profile":
		payload = map[string]any{"profile": "fake profile"}
	default:
		return 0, fmt.Errorf("unknown schema: %s", schemaName)
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(b, result); err != nil {
		return 0, err
	}
	return fc.Tokens, nil
}

// CompleteWithCorrection delegates to Complete, ignoring correction context.
func (fc *FakeCompleter) CompleteWithCorrection(ctx context.Context, system, user, _, _ string, schemaName string, schema json.RawMessage, result any) (int, error) {
	// System prompt capture happens inside Complete.
	return fc.Complete(ctx, system, user, schemaName, schema, result)
}

// FakeEmbedder returns deterministic, normalized vectors derived from input text hash.
type FakeEmbedder struct {
	Dim int
}

// Embed returns a deterministic unit vector for the given text.
func (fe *FakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()

	vec := make([]float32, fe.Dim)
	var sumSq float64
	for i := range vec {
		bits := seed ^ uint64(i)*2654435761
		vec[i] = float32(bits%1000)/500.0 - 1.0
		sumSq += float64(vec[i]) * float64(vec[i])
	}
	norm := float32(math.Sqrt(sumSq))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}

// EmbedBatch returns deterministic vectors for each text.
func (fe *FakeEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := fe.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

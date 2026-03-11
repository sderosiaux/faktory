package faktory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sderosiaux/faktory/faktorytest"
)

func newTestMemoryImportance(t *testing.T, fc *faktorytest.FakeCompleter) *Memory {
	t.Helper()
	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath: db, EmbedDimension: 8,
		Completer: fc, TextEmbedder: &faktorytest.FakeEmbedder{Dim: 8},
		DisableGraph: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

func TestImportance_HighScoredFactsResistDecay(t *testing.T) {
	created := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	facts := []Fact{
		{ID: "high", Text: "important", Score: 1.0, CreatedAt: created, Importance: 5},
		{ID: "low", Text: "trivial", Score: 1.0, CreatedAt: created, Importance: 1},
	}
	applyDecay(facts, 0.01, 0.1)
	if facts[0].ID != "high" {
		t.Errorf("high-importance fact should rank first, got %s", facts[0].ID)
	}
	ratio := facts[0].Score / facts[1].Score
	if ratio < 1.5 {
		t.Errorf("importance=5 vs importance=1 ratio = %.2f, want >= 1.5", ratio)
	}
}

func TestImportance_StoredAndRetrieved(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 5},
			{Text: "weather is nice", Importance: 1},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "weather is nice", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryImportance(t, fc)
	_, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Go. The weather is nice."},
	}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := mem.GetAll(context.Background(), "u1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) < 2 {
		t.Fatalf("got %d facts, want 2", len(facts))
	}
	importanceByText := map[string]int{}
	for _, f := range facts {
		importanceByText[f.Text] = f.Importance
	}
	if importanceByText["likes Go"] != 5 {
		t.Errorf("likes Go importance = %d, want 5", importanceByText["likes Go"])
	}
	if importanceByText["weather is nice"] != 1 {
		t.Errorf("weather is nice importance = %d, want 1", importanceByText["weather is nice"])
	}
}

func TestImportance_DefaultImportanceIs3(t *testing.T) {
	// Use a timestamp 10 days in the past so time drift between calls is negligible.
	created := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	facts := []Fact{
		{ID: "f1", Text: "test", Score: 1.0, CreatedAt: created, Importance: 3},
	}
	factsCopy := []Fact{
		{ID: "f1", Text: "test", Score: 1.0, CreatedAt: created, Importance: 0},
	}
	applyDecay(facts, 0.01, 0.1)
	applyDecay(factsCopy, 0.01, 0.1)
	diff := facts[0].Score - factsCopy[0].Score
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Errorf("importance=0 should be treated as 3: score3=%.12f score0=%.12f diff=%.2e", facts[0].Score, factsCopy[0].Score, diff)
	}
}

func TestImportance_ExtractionSchemaValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(factExtractionSchema, &schema); err != nil {
		t.Fatalf("schema parse error: %v", err)
	}
	props := schema["properties"].(map[string]any)
	factsField := props["facts"].(map[string]any)
	items, ok := factsField["items"].(map[string]any)
	if !ok {
		t.Fatal("facts items should be an object (not string)")
	}
	itemProps := items["properties"].(map[string]any)
	if _, ok := itemProps["text"]; !ok {
		t.Error("facts items missing text property")
	}
	if _, ok := itemProps["importance"]; !ok {
		t.Error("facts items missing importance property")
	}
}

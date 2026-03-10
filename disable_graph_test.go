package faktory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

func newTestMemoryWithDisableGraph(t *testing.T, fc *faktorytest.FakeCompleter, disableGraph bool) *Memory {
	t.Helper()
	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath:         db,
		EmbedDimension: 8,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
		DisableGraph:   disableGraph,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

func TestDisableGraph_NoEntityExtraction(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []string{"likes Go", "lives in Lyon"},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{
			{Name: "Lyon", Type: "place"},
		},
		Relations: []faktorytest.RelationResult{
			{Source: "User", Relation: "lives_in", Target: "Lyon"},
		},
		Tokens: 10,
	}
	mem := newTestMemoryWithDisableGraph(t, fc, true)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Go and I live in Lyon"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// entity_extraction should never be called
	if count := fc.GetCallCount("entity_extraction"); count != 0 {
		t.Errorf("entity_extraction call count = %d, want 0", count)
	}

	// Fact pipeline should still work
	if len(result.Added) == 0 {
		t.Error("expected facts to be added, got none")
	}

	// No entities in DB
	entities, err := mem.store.GetAllEntities("u1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 0 {
		t.Errorf("entities = %d, want 0 (graph disabled)", len(entities))
	}

	// Result should have no extracted entities/relations
	if len(result.ExtractedEntities) != 0 {
		t.Errorf("ExtractedEntities = %d, want 0", len(result.ExtractedEntities))
	}
	if len(result.ExtractedRelations) != 0 {
		t.Errorf("ExtractedRelations = %d, want 0", len(result.ExtractedRelations))
	}
}

func TestDisableGraph_RecallStillWorks(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []string{"prefers dark roast coffee"},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "prefers dark roast coffee", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithDisableGraph(t, fc, true)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I prefer dark roast coffee"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Search should return facts even with graph disabled
	facts, err := mem.Search(ctx, "coffee", "u1", 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(facts) == 0 {
		t.Error("expected Search to return facts, got none")
	}

	// Recall should also work
	recall, err := mem.Recall(ctx, "coffee", "u1", nil)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(recall.Facts) == 0 {
		t.Error("expected Recall to return facts, got none")
	}
}

func TestDisableGraph_False_GraphRuns(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []string{"speaks French"},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "speaks French", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{
			{Name: "Alice", Type: "person"},
		},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithDisableGraph(t, fc, false)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "Alice speaks French"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// entity_extraction should be called when graph is enabled
	if count := fc.GetCallCount("entity_extraction"); count == 0 {
		t.Error("entity_extraction should be called when DisableGraph=false")
	}

	// Entities should exist in DB
	entities, err := mem.store.GetAllEntities("u1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) == 0 {
		t.Error("expected entities in DB when graph is enabled")
	}

	// Result should report extracted entities
	if len(result.ExtractedEntities) == 0 {
		t.Error("ExtractedEntities should be non-empty when graph is enabled")
	}
}

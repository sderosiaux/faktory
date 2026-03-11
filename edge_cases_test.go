package faktory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

// newTestMemoryWithFake creates a Memory backed by an in-temp-dir SQLite DB
// and the given FakeCompleter + FakeEmbedder.
func newTestMemoryWithFake(t *testing.T, fc *faktorytest.FakeCompleter) *Memory {
	t.Helper()
	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath:         db,
		EmbedDimension: 8,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

// --- Fact pipeline edge cases ---

func TestAdd_EmptyFactExtraction(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{},
		Reconcile: []faktorytest.ReconcileAction{},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    5,
	}
	mem := newTestMemoryWithFake(t, fc)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "the weather is nice today"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Added) != 0 {
		t.Errorf("Added = %d, want 0", len(result.Added))
	}
	if len(result.Updated) != 0 {
		t.Errorf("Updated = %d, want 0", len(result.Updated))
	}
	if len(result.Deleted) != 0 {
		t.Errorf("Deleted = %d, want 0", len(result.Deleted))
	}
}

func TestAdd_ReconciliationHallucinatedID(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			// ID "999" does not exist in the integer mapping.
			{ID: "999", Text: "likes Go updated", Event: "UPDATE"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newTestMemoryWithFake(t, fc)

	// Pre-populate with a fact so the KNN search returns a "similar" hit,
	// forcing the candidate through the reconciliation path.
	fe := &faktorytest.FakeEmbedder{Dim: 8}
	emb, _ := fe.Embed(context.Background(), "likes Go")
	if _, err := mem.store.InsertFact("u1", "", "likes Go old", hashFact("likes Go old"), emb, 3); err != nil {
		t.Fatal(err)
	}

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Go"},
	}, "u1")
	if err != nil {
		t.Fatalf("should not error on hallucinated ID: %v", err)
	}
	// The UPDATE with hallucinated ID "999" is silently skipped.
	if len(result.Added) != 0 {
		t.Errorf("Added = %d, want 0 (hallucinated ID skipped)", len(result.Added))
	}
	if len(result.Updated) != 0 {
		t.Errorf("Updated = %d, want 0 (hallucinated ID skipped)", len(result.Updated))
	}
}

func TestAdd_ReconciliationInvalidEvent(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes Rust", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			// "MERGE" is not a valid event type.
			{ID: "0", Text: "likes Rust", Event: "MERGE"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newTestMemoryWithFake(t, fc)

	// Pre-populate with a similar fact so the candidate goes through reconciliation.
	fe := &faktorytest.FakeEmbedder{Dim: 8}
	emb, _ := fe.Embed(context.Background(), "likes Rust")
	if _, err := mem.store.InsertFact("u1", "", "likes Rust old", hashFact("likes Rust old"), emb, 3); err != nil {
		t.Fatal(err)
	}

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Rust"},
	}, "u1")
	if err != nil {
		t.Fatalf("should not error on invalid event type: %v", err)
	}
	// The MERGE action falls through the switch — nothing stored.
	if len(result.Added) != 0 {
		t.Errorf("Added = %d, want 0", len(result.Added))
	}
	if len(result.Updated) != 0 {
		t.Errorf("Updated = %d, want 0", len(result.Updated))
	}
	if len(result.Deleted) != 0 {
		t.Errorf("Deleted = %d, want 0", len(result.Deleted))
	}
}

func TestAdd_DuplicateHashSkipsReconciliation(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes pizza", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "likes pizza", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	// First Add: facts are new, full pipeline runs.
	msg := []Message{{Role: "user", Content: "I like pizza"}}
	_, err := mem.Add(ctx, msg, "u1")
	if err != nil {
		t.Fatalf("first Add failed: %v", err)
	}

	callsBefore := fc.GetCallCount("reconcile_memory")

	// Second Add with different messages but the FakeCompleter still returns
	// the same fact text. Since "likes pizza" hash already exists in the store,
	// the hash-filter should remove it before reconciliation.
	msg2 := []Message{{Role: "user", Content: "I really like pizza a lot"}}
	result, err := mem.Add(ctx, msg2, "u1")
	if err != nil {
		t.Fatalf("second Add failed: %v", err)
	}

	callsAfter := fc.GetCallCount("reconcile_memory")
	if callsAfter != callsBefore {
		t.Errorf("reconcile_memory called %d times after dedup, want %d (should skip)", callsAfter, callsBefore)
	}

	// Nothing new should be added/updated.
	if len(result.Added) != 0 {
		t.Errorf("Added = %d, want 0 on duplicate", len(result.Added))
	}
}

// --- Graph pipeline edge cases ---

func TestAdd_GraphPipelinePronounsFiltered(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "speaks French", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "speaks French", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{
			{Name: "I", Type: "person"},
			{Name: "he", Type: "person"},
			{Name: "my", Type: "person"},
			{Name: "Alice", Type: "person"},
		},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I speak French"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only "Alice" should be stored; pronouns are filtered.
	entities, err := mem.store.GetAllEntities("u1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		if isPronoun(e.Name) {
			t.Errorf("pronoun %q should not be stored as entity", e.Name)
		}
	}
	if len(entities) != 1 {
		t.Errorf("stored entities = %d, want 1 (only Alice)", len(entities))
	}

	// ExtractedEntities reports all 4 (raw LLM output), but DB only has 1.
	if len(result.ExtractedEntities) != 4 {
		t.Errorf("ExtractedEntities = %d, want 4 (raw LLM output)", len(result.ExtractedEntities))
	}
}

func TestAdd_GraphPipelineEmpty(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes hiking", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "likes hiking", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like hiking"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fact pipeline should succeed normally.
	if len(result.Added) != 1 {
		t.Errorf("Added = %d, want 1", len(result.Added))
	}
	// Graph portion should be empty but no error.
	if len(result.ExtractedEntities) != 0 {
		t.Errorf("ExtractedEntities = %d, want 0", len(result.ExtractedEntities))
	}
	if len(result.ExtractedRelations) != 0 {
		t.Errorf("ExtractedRelations = %d, want 0", len(result.ExtractedRelations))
	}
	if len(result.GraphErrors) != 0 {
		t.Errorf("GraphErrors = %v, want empty", result.GraphErrors)
	}
}

func TestAdd_GraphPipelineFailureNonFatal(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "lives in Tokyo", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "lives in Tokyo", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
		Errors: map[string]error{
			"entity_extraction": fmt.Errorf("simulated entity extraction failure"),
		},
	}
	mem := newTestMemoryWithFake(t, fc)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I live in Tokyo"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add should succeed even when graph pipeline fails: %v", err)
	}

	// Fact pipeline should still work.
	if len(result.Added) != 1 {
		t.Errorf("Added = %d, want 1", len(result.Added))
	}
	if result.Added[0].Text != "lives in Tokyo" {
		t.Errorf("Added[0].Text = %q, want %q", result.Added[0].Text, "lives in Tokyo")
	}

	// GraphErrors should capture the failure.
	if len(result.GraphErrors) == 0 {
		t.Fatal("expected GraphErrors to be non-empty")
	}
}

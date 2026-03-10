package faktory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

// Compile-time interface checks
var _ Completer = (*LLM)(nil)
var _ TextEmbedder = (*Embedder)(nil)

func TestLLMSatisfiesCompleter(t *testing.T) {
	// Compile-time check above is sufficient; this test verifies
	// the constructor returns a usable value.
	c := NewLLM("http://localhost", "", "test", nil)
	if c == nil {
		t.Fatal("NewLLM should return a non-nil *LLM")
	}
}

func TestEmbedderSatisfiesTextEmbedder(t *testing.T) {
	e := NewEmbedder("http://localhost", "", "test", 8, nil)
	if e == nil {
		t.Fatal("NewEmbedder should return a non-nil *Embedder")
	}
}

func TestAddWithFakes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	mem, err := New(Config{
		DBPath:         dbPath,
		EmbedDimension: 8,
		Completer: &faktorytest.FakeCompleter{
			Facts: []string{"likes Go", "lives in Paris"},
			Reconcile: []faktorytest.ReconcileAction{
				{ID: "0", Text: "likes Go", Event: "ADD"},
				{ID: "1", Text: "lives in Paris", Event: "ADD"},
			},
			Entities: []faktorytest.EntityResult{
				{Name: "Alice", Type: "person"},
				{Name: "Paris", Type: "place"},
			},
			Relations: []faktorytest.RelationResult{
				{Source: "Alice", Relation: "lives_in", Target: "Paris"},
			},
			Tokens: 100,
		},
		TextEmbedder: &faktorytest.FakeEmbedder{Dim: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dbPath)
	defer mem.Close()

	ctx := context.Background()
	result, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice, I like Go and live in Paris"},
	}, "user-1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if len(result.Added) != 2 {
		t.Errorf("added %d facts, want 2", len(result.Added))
	}
	if result.Tokens == 0 {
		t.Error("expected non-zero token count")
	}
}

func TestSearchWithFakes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	mem, err := New(Config{
		DBPath:         dbPath,
		EmbedDimension: 8,
		Completer: &faktorytest.FakeCompleter{
			Facts: []string{"likes Go"},
			Reconcile: []faktorytest.ReconcileAction{
				{ID: "0", Text: "likes Go", Event: "ADD"},
			},
			Entities: []faktorytest.EntityResult{},
			Tokens:   50,
		},
		TextEmbedder: &faktorytest.FakeEmbedder{Dim: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dbPath)
	defer mem.Close()

	ctx := context.Background()
	_, err = mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go"},
	}, "user-1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	facts, err := mem.Search(ctx, "Go", "user-1", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(facts) == 0 {
		t.Error("expected at least one fact from search")
	}
}

func TestAddResultContainsExtractedFacts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	mem, err := New(Config{
		DBPath:         dbPath,
		EmbedDimension: 8,
		Completer: &faktorytest.FakeCompleter{
			Facts: []string{"likes pizza", "speaks French"},
			Reconcile: []faktorytest.ReconcileAction{
				{ID: "0", Text: "likes pizza", Event: "ADD"},
				{ID: "1", Text: "speaks French", Event: "ADD"},
			},
			Entities: []faktorytest.EntityResult{
				{Name: "Alice", Type: "person"},
			},
			Relations: []faktorytest.RelationResult{
				{Source: "Alice", Relation: "speaks", Target: "French"},
			},
			Tokens: 30,
		},
		TextEmbedder: &faktorytest.FakeEmbedder{Dim: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dbPath)
	defer mem.Close()

	ctx := context.Background()
	result, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice, I like pizza and speak French"},
	}, "user-1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify ExtractedFacts is populated
	if len(result.ExtractedFacts) != 2 {
		t.Errorf("ExtractedFacts = %d, want 2", len(result.ExtractedFacts))
	}

	// Verify ExtractedEntities is populated
	if len(result.ExtractedEntities) != 1 {
		t.Errorf("ExtractedEntities = %d, want 1", len(result.ExtractedEntities))
	}
	if len(result.ExtractedEntities) > 0 && result.ExtractedEntities[0].Name != "Alice" {
		t.Errorf("entity name = %q, want Alice", result.ExtractedEntities[0].Name)
	}

	// Verify ExtractedRelations is populated
	if len(result.ExtractedRelations) != 1 {
		t.Errorf("ExtractedRelations = %d, want 1", len(result.ExtractedRelations))
	}
	if len(result.ExtractedRelations) > 0 && result.ExtractedRelations[0].Target != "French" {
		t.Errorf("relation target = %q, want French", result.ExtractedRelations[0].Target)
	}
}

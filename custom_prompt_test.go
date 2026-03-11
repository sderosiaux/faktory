package faktory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

func newPromptTestMemory(t *testing.T, fc *faktorytest.FakeCompleter, cfg Config) *Memory {
	t.Helper()
	cfg.DBPath = filepath.Join(t.TempDir(), "test.db")
	cfg.EmbedDimension = 8
	cfg.Completer = fc
	cfg.TextEmbedder = &faktorytest.FakeEmbedder{Dim: 8}
	mem, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

// seedSimilarFact inserts a fact with different text but the same embedding as
// matchText, so similarity search returns it with score 1.0 and triggers
// reconciliation instead of the novel fast-path.
func seedSimilarFact(t *testing.T, mem *Memory, userID, storedText, matchText string) {
	t.Helper()
	emb, err := mem.embedder.Embed(context.Background(), matchText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.store.InsertFact(userID, "", storedText, hashFact(storedText), emb, 3, "", 0); err != nil {
		t.Fatal(err)
	}
}

func defaultFakeCompleter() *faktorytest.FakeCompleter {
	return &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "likes Go", Event: "ADD"},
		},
		Entities:      []faktorytest.EntityResult{{Name: "Alice", Type: "person"}},
		Relations:     []faktorytest.RelationResult{},
		Tokens:        10,
		SystemPrompts: make(map[string]string),
	}
}

func TestCustomFactExtractionPrompt(t *testing.T) {
	fc := defaultFakeCompleter()
	custom := "my custom fact extraction prompt"
	mem := newPromptTestMemory(t, fc, Config{PromptFactExtraction: custom})

	_, err := mem.Add(context.Background(), []Message{{Role: "user", Content: "hello"}}, "u1")
	if err != nil {
		t.Fatal(err)
	}

	got := fc.GetSystemPrompt("fact_extraction")
	if got != custom {
		t.Errorf("fact_extraction prompt = %q, want %q", got, custom)
	}
}

func TestCustomReconciliationPrompt(t *testing.T) {
	fc := defaultFakeCompleter()
	custom := "my custom reconciliation prompt"
	mem := newPromptTestMemory(t, fc, Config{PromptReconciliation: custom})

	// Seed store with a fact that shares the same embedding as "likes Go"
	// so similarity gate triggers reconciliation.
	seedSimilarFact(t, mem, "u1", "old: likes Go", "likes Go")

	_, err := mem.Add(context.Background(), []Message{{Role: "user", Content: "hello"}}, "u1")
	if err != nil {
		t.Fatal(err)
	}

	got := fc.GetSystemPrompt("reconcile_memory")
	if got != custom {
		t.Errorf("reconcile_memory prompt = %q, want %q", got, custom)
	}
}

func TestCustomEntityExtractionPrompt(t *testing.T) {
	fc := defaultFakeCompleter()
	custom := "my custom entity extraction prompt"
	mem := newPromptTestMemory(t, fc, Config{PromptEntityExtraction: custom})

	_, err := mem.Add(context.Background(), []Message{{Role: "user", Content: "hello"}}, "u1")
	if err != nil {
		t.Fatal(err)
	}

	got := fc.GetSystemPrompt("entity_extraction")
	if got != custom {
		t.Errorf("entity_extraction prompt = %q, want %q", got, custom)
	}
}

func TestDefaultPromptsWhenCustomEmpty(t *testing.T) {
	fc := defaultFakeCompleter()
	mem := newPromptTestMemory(t, fc, Config{})

	// Seed store with a fact that shares the same embedding as "likes Go"
	// so similarity gate triggers reconciliation.
	seedSimilarFact(t, mem, "u1", "old: likes Go", "likes Go")

	_, err := mem.Add(context.Background(), []Message{{Role: "user", Content: "hello"}}, "u1")
	if err != nil {
		t.Fatal(err)
	}

	if got := fc.GetSystemPrompt("fact_extraction"); got != factExtractionPrompt {
		t.Errorf("fact_extraction prompt not default: got %q", got)
	}
	if got := fc.GetSystemPrompt("reconcile_memory"); got != reconcilePrompt {
		t.Errorf("reconcile_memory prompt not default: got %q", got)
	}
	if got := fc.GetSystemPrompt("entity_extraction"); got != entityExtractionPrompt {
		t.Errorf("entity_extraction prompt not default: got %q", got)
	}
}

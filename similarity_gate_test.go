package faktory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

// newGateTestMemory creates a Memory backed by a temp SQLite DB
// with the given FakeCompleter and 8-dim FakeEmbedder.
func newGateTestMemory(t *testing.T, fc *faktorytest.FakeCompleter) *Memory {
	t.Helper()
	return newGateTestMemoryOpts(t, fc, false)
}

func newGateTestMemoryOpts(t *testing.T, fc *faktorytest.FakeCompleter, disableGraph bool) *Memory {
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

// seedFactWithEmbedding inserts a fact into the store using the embedding
// of matchText (so KNN search returns it with score 1.0 for matchText queries).
func seedFactWithEmbedding(t *testing.T, mem *Memory, userID, ns, storedText, matchText string) {
	t.Helper()
	emb, err := mem.embedder.Embed(context.Background(), matchText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.store.InsertFact(userID, ns, storedText, hashFact(storedText), emb, 3); err != nil {
		t.Fatal(err)
	}
}

func TestSimilarityGateSkipsReconciliation(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes Go", Importance: 3}, {Text: "lives in Paris", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "likes Go", Event: "ADD"},
			{ID: "1", Text: "lives in Paris", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newGateTestMemory(t, fc)

	// Empty store: no existing facts -> all candidates are novel.
	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Go and live in Paris"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Both facts should be added via the novel fast-path.
	if len(result.Added) != 2 {
		t.Errorf("Added = %d, want 2", len(result.Added))
	}

	// Reconciliation should NOT have been called.
	calls := fc.GetCallCount("reconcile_memory")
	if calls != 0 {
		t.Errorf("reconcile_memory called %d times, want 0 (novel facts skip reconciliation)", calls)
	}

	// Verify facts are actually in the store.
	facts, err := mem.store.GetAllFacts("u1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Errorf("stored facts = %d, want 2", len(facts))
	}
}

func TestSimilarityGateSendsHighSimilarityToReconciliation(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "likes Go a lot", Event: "UPDATE"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newGateTestMemory(t, fc)

	// Seed the store with a fact that has the same embedding as "likes Go".
	// This makes KNN return it with cosine distance 0 (score 1.0 >= 0.5).
	seedFactWithEmbedding(t, mem, "u1", "", "old: likes Go", "likes Go")

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "I like Go"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Reconciliation should have been called.
	calls := fc.GetCallCount("reconcile_memory")
	if calls != 1 {
		t.Errorf("reconcile_memory called %d times, want 1", calls)
	}

	// The UPDATE should have been applied.
	if len(result.Updated) != 1 {
		t.Errorf("Updated = %d, want 1", len(result.Updated))
	}
	if len(result.Updated) > 0 && result.Updated[0].Text != "likes Go a lot" {
		t.Errorf("Updated[0].Text = %q, want %q", result.Updated[0].Text, "likes Go a lot")
	}
}

func TestReconciliationContextCap(t *testing.T) {
	// Create a FakeCompleter that extracts one fact and returns ADD for it.
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "new fact", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "new fact", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem := newGateTestMemory(t, fc)

	// Seed the store with 35 facts, all sharing the same embedding as "new fact"
	// so they all appear as similar existing facts.
	for i := 0; i < 35; i++ {
		storedText := fmt.Sprintf("existing fact #%d", i)
		seedFactWithEmbedding(t, mem, "u1", "", storedText, "new fact")
	}

	_, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "here is a new fact"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Reconciliation should have been called.
	calls := fc.GetCallCount("reconcile_memory")
	if calls != 1 {
		t.Fatalf("reconcile_memory called %d times, want 1", calls)
	}

	// Verify the reconciliation input was capped at 30 existing facts.
	// The user prompt sent to reconcile_memory contains "Existing facts:\n..."
	userPrompt := fc.GetUserPrompt("reconcile_memory")
	if userPrompt == "" {
		t.Fatal("reconcile_memory user prompt not captured")
	}

	// Count lines in the "Existing facts:" section.
	parts := strings.SplitN(userPrompt, "\n\nNew facts:", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected reconcile input format: %q", userPrompt)
	}
	existingSection := parts[0]
	// Remove the "Existing facts:" header line
	existingLines := strings.Split(existingSection, "\n")
	// First line is "Existing facts:", rest are the fact lines
	factLines := 0
	for _, line := range existingLines {
		if strings.HasPrefix(line, "id: ") {
			factLines++
		}
	}

	if factLines > 30 {
		t.Errorf("existing facts in reconciliation = %d, want <= 30 (context cap)", factLines)
	}
	if factLines == 0 {
		t.Error("expected at least some existing facts in reconciliation input")
	}
}

func TestChunkedReconciliation(t *testing.T) {
	// Generate 25 extracted facts: 15 will need reconciliation, 10 will be novel.
	const totalFacts = 25
	const reconFacts = 15

	var facts []faktorytest.FactResult
	for i := 0; i < totalFacts; i++ {
		facts = append(facts, faktorytest.FactResult{Text: fmt.Sprintf("fact-%d", i), Importance: 3})
	}

	// ReconcileFunc parses the "New facts:" section from the user prompt and
	// returns an ADD action for each new fact it sees.
	reconcileFunc := func(userPrompt string) []faktorytest.ReconcileAction {
		parts := strings.SplitN(userPrompt, "\n\nNew facts:\n", 2)
		if len(parts) != 2 {
			return nil
		}
		var actions []faktorytest.ReconcileAction
		for _, line := range strings.Split(strings.TrimSpace(parts[1]), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			actions = append(actions, faktorytest.ReconcileAction{
				Text:  line,
				Event: "ADD",
			})
		}
		return actions
	}

	fc := &faktorytest.FakeCompleter{
		Facts:         facts,
		ReconcileFunc: reconcileFunc,
		Entities:      []faktorytest.EntityResult{},
		Tokens:        10,
	}
	mem := newGateTestMemoryOpts(t, fc, true)

	// Seed existing facts so the first 15 extracted facts find similar matches
	// (embedding matches force score >= 0.5, routing them to reconciliation).
	for i := 0; i < reconFacts; i++ {
		seedFactWithEmbedding(t, mem, "u1", "",
			fmt.Sprintf("old-fact-%d", i),
			fmt.Sprintf("fact-%d", i))
	}

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "25 facts about me"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// All 25 should be added (10 novel + 15 via reconciliation ADD).
	if len(result.Added) != totalFacts {
		t.Errorf("Added = %d, want %d", len(result.Added), totalFacts)
	}

	// Reconciliation should have been called at least 2 times (15 > maxReconcileChunk=10).
	calls := fc.GetCallCount("reconcile_memory")
	if calls < 2 {
		t.Errorf("reconcile_memory called %d times, want >= 2 (chunked)", calls)
	}

	// Verify exactly 2 chunks: ceil(15/10) = 2.
	if calls != 2 {
		t.Errorf("reconcile_memory called %d times, want exactly 2 for 15 candidates with chunk size 10", calls)
	}

	// Verify all facts are in the store.
	stored, err := mem.store.GetAllFacts("u1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	// 15 seeded (old-fact-*) + 25 added = 40, but reconcile ADD creates new rows
	// while the old seeded rows remain. The 10 novel are inserted directly.
	// The 15 reconciled are ADD actions (new inserts, old ones untouched).
	// Total stored = 15 (seeded) + 10 (novel) + 15 (reconcile ADD) = 40.
	if len(stored) != reconFacts+totalFacts {
		t.Errorf("stored facts = %d, want %d", len(stored), reconFacts+totalFacts)
	}
}

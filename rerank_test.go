package faktory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

var errFakeRerank = fmt.Errorf("fake rerank error")

func TestRerank_DisabledByDefault(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
			{Text: "lives in Lyon", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go and I live in Lyon"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Recall without Rerank — should NOT call the "rerank" schema
	_, err = mem.Recall(ctx, "Go programming", "u1", nil)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if count := fc.GetCallCount("rerank"); count != 0 {
		t.Errorf("rerank call count = %d, want 0 (disabled by default)", count)
	}
}

func TestRerank_NotCalledWithExplicitFalse(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, err = mem.Recall(ctx, "Go", "u1", &RecallOptions{Rerank: false})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if count := fc.GetCallCount("rerank"); count != 0 {
		t.Errorf("rerank call count = %d, want 0 (Rerank=false)", count)
	}
}

func TestRerank_CalledWhenEnabled(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
			{Text: "lives in Lyon", Importance: 3},
			{Text: "works at Stripe", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
			{Text: "works at Stripe", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	res, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go, live in Lyon, and work at Stripe"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(res.Added) < 3 {
		t.Fatalf("expected 3 facts added, got %d", len(res.Added))
	}

	// Set RerankIDs to reverse order
	fc.RerankIDs = []string{res.Added[2].ID, res.Added[0].ID, res.Added[1].ID}

	recall, err := mem.Recall(ctx, "programming", "u1", &RecallOptions{Rerank: true})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if count := fc.GetCallCount("rerank"); count != 1 {
		t.Errorf("rerank call count = %d, want 1", count)
	}

	// Verify reranked order matches RerankIDs
	if len(recall.Facts) < 3 {
		t.Fatalf("expected at least 3 facts, got %d", len(recall.Facts))
	}
	if recall.Facts[0].ID != res.Added[2].ID {
		t.Errorf("first fact ID = %s, want %s", recall.Facts[0].ID, res.Added[2].ID)
	}
	if recall.Facts[1].ID != res.Added[0].ID {
		t.Errorf("second fact ID = %s, want %s", recall.Facts[1].ID, res.Added[0].ID)
	}
	if recall.Facts[2].ID != res.Added[1].ID {
		t.Errorf("third fact ID = %s, want %s", recall.Facts[2].ID, res.Added[1].ID)
	}
}

func TestRerank_FallbackOnError(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
			{Text: "lives in Lyon", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Errors:    map[string]error{"rerank": errFakeRerank},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go and I live in Lyon"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Recall with Rerank=true but error injected — should NOT fail
	recall, err := mem.Recall(ctx, "Go", "u1", &RecallOptions{Rerank: true})
	if err != nil {
		t.Fatalf("Recall should not fail on rerank error: %v", err)
	}

	// Should still return facts (original order)
	if len(recall.Facts) == 0 {
		t.Error("expected facts even when rerank errors")
	}
}

func TestRerank_MissingIDsAppended(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
			{Text: "lives in Lyon", Importance: 3},
			{Text: "works at Stripe", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
			{Text: "works at Stripe", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	res, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go, live in Lyon, work at Stripe"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(res.Added) < 3 {
		t.Fatalf("expected 3 facts, got %d", len(res.Added))
	}

	// Only return 1 of the 3 IDs — the other 2 should be appended
	fc.RerankIDs = []string{res.Added[1].ID}

	recall, err := mem.Recall(ctx, "programming", "u1", &RecallOptions{Rerank: true})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if len(recall.Facts) < 3 {
		t.Fatalf("expected 3 facts (1 ranked + 2 appended), got %d", len(recall.Facts))
	}

	// First fact should be the one the LLM ranked
	if recall.Facts[0].ID != res.Added[1].ID {
		t.Errorf("first fact ID = %s, want %s (the ranked one)", recall.Facts[0].ID, res.Added[1].ID)
	}

	// Remaining facts should include the unranked ones
	seen := map[string]bool{}
	for _, f := range recall.Facts {
		seen[f.ID] = true
	}
	for _, a := range res.Added {
		if !seen[a.ID] {
			t.Errorf("fact %s missing from reranked result", a.ID)
		}
	}
}

func TestRerank_PromptContainsQueryAndFacts(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "likes Go", Importance: 3},
			{Text: "lives in Lyon", Importance: 3},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{Text: "likes Go", Event: "ADD"},
			{Text: "lives in Lyon", Event: "ADD"},
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go and I live in Lyon"},
	}, "u1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, err = mem.Recall(ctx, "programming language", "u1", &RecallOptions{Rerank: true})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	prompt := fc.GetUserPrompt("rerank")
	if prompt == "" {
		t.Fatal("rerank user prompt is empty")
	}

	// Prompt should contain the query
	if !strings.Contains(prompt, "programming language") {
		t.Errorf("rerank prompt missing query, got: %s", prompt)
	}

	// Prompt should contain fact texts
	if !strings.Contains(prompt, "likes Go") {
		t.Errorf("rerank prompt missing fact text 'likes Go', got: %s", prompt)
	}
	if !strings.Contains(prompt, "lives in Lyon") {
		t.Errorf("rerank prompt missing fact text 'lives in Lyon', got: %s", prompt)
	}
}

func TestRerank_EmptyFacts_Skipped(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{},
		Reconcile: []faktorytest.ReconcileAction{},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	// No facts added — Recall with Rerank should not call the LLM
	recall, err := mem.Recall(ctx, "anything", "u1", &RecallOptions{Rerank: true})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if count := fc.GetCallCount("rerank"); count != 0 {
		t.Errorf("rerank should not be called when no facts, got %d calls", count)
	}
	if len(recall.Facts) != 0 {
		t.Errorf("expected 0 facts, got %d", len(recall.Facts))
	}
}

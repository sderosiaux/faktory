package faktory

import (
	"context"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

func TestAddResult_TotalFacts(t *testing.T) {
	callCount := 0
	fc := &faktorytest.FakeCompleter{
		Facts: []string{"likes Go", "lives in Paris"},
		ReconcileFunc: func(_ string) []faktorytest.ReconcileAction {
			callCount++
			if callCount == 1 {
				return []faktorytest.ReconcileAction{
					{ID: "0", Text: "likes Go", Event: "ADD"},
					{ID: "1", Text: "lives in Paris", Event: "ADD"},
				}
			}
			return []faktorytest.ReconcileAction{
				{ID: "0", Text: "speaks French", Event: "ADD"},
				{ID: "1", Text: "plays piano", Event: "ADD"},
			}
		},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    10,
	}
	mem := newTestMemoryWithFake(t, fc)
	ctx := context.Background()

	// First Add: 2 facts inserted.
	r1, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I like Go and live in Paris"},
	}, "u1")
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if r1.TotalFacts != 2 {
		t.Errorf("first Add TotalFacts = %d, want 2", r1.TotalFacts)
	}

	// Second Add with different facts — change what the fake returns.
	fc.Facts = []string{"speaks French", "plays piano"}
	r2, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I speak French and play piano"},
	}, "u1")
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if r2.TotalFacts != 4 {
		t.Errorf("second Add TotalFacts = %d, want 4", r2.TotalFacts)
	}
}

func TestAddResult_TotalFacts_EmptyExtraction(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []string{},
		Entities:  []faktorytest.EntityResult{},
		Relations: []faktorytest.RelationResult{},
		Tokens:    5,
	}
	mem := newTestMemoryWithFake(t, fc)

	result, err := mem.Add(context.Background(), []Message{
		{Role: "user", Content: "the weather is nice"},
	}, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No facts extracted, TotalFacts should be 0.
	if result.TotalFacts != 0 {
		t.Errorf("TotalFacts = %d, want 0", result.TotalFacts)
	}
}

func TestCountFacts(t *testing.T) {
	mem := newTestMemoryWithFake(t, &faktorytest.FakeCompleter{})
	fe := &faktorytest.FakeEmbedder{Dim: 8}
	ctx := context.Background()

	count, err := mem.store.CountFacts("u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}

	emb, _ := fe.Embed(ctx, "fact one")
	if _, err := mem.store.InsertFact("u1", "", "fact one", hashFact("fact one"), emb); err != nil {
		t.Fatal(err)
	}
	emb, _ = fe.Embed(ctx, "fact two")
	if _, err := mem.store.InsertFact("u1", "", "fact two", hashFact("fact two"), emb); err != nil {
		t.Fatal(err)
	}

	count, err = mem.store.CountFacts("u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	// Different namespace should not be counted.
	count, err = mem.store.CountFacts("u1", "other")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("count for other namespace = %d, want 0", count)
	}
}

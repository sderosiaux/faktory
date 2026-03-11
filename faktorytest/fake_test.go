package faktorytest

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFakeCompleter_FactExtraction(t *testing.T) {
	fc := &FakeCompleter{
		Facts:  []FactResult{{Text: "likes pizza", Importance: 3}, {Text: "lives in Paris", Importance: 4}},
		Tokens: 42,
	}

	var result struct {
		Facts []FactResult `json:"facts"`
	}
	tokens, err := fc.Complete(context.Background(), "system", "user", "fact_extraction", nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 42 {
		t.Errorf("tokens = %d, want 42", tokens)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("got %d facts, want 2", len(result.Facts))
	}
	if result.Facts[0].Text != "likes pizza" {
		t.Errorf("fact[0].Text = %q, want %q", result.Facts[0].Text, "likes pizza")
	}
}

func TestFakeCompleter_Reconcile(t *testing.T) {
	fc := &FakeCompleter{
		Reconcile: []ReconcileAction{
			{ID: "0", Text: "likes pizza", Event: "ADD"},
			{ID: "1", Text: "lives in Lyon", Event: "UPDATE"},
		},
		Tokens: 10,
	}

	var result struct {
		Memory []ReconcileAction `json:"memory"`
	}
	tokens, err := fc.Complete(context.Background(), "system", "user", "reconcile_memory", nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 10 {
		t.Errorf("tokens = %d, want 10", tokens)
	}
	if len(result.Memory) != 2 {
		t.Fatalf("got %d actions, want 2", len(result.Memory))
	}
	if result.Memory[0].Event != "ADD" {
		t.Errorf("action[0].Event = %q, want ADD", result.Memory[0].Event)
	}
}

func TestFakeCompleter_EntityExtraction(t *testing.T) {
	fc := &FakeCompleter{
		Entities: []EntityResult{
			{Name: "Alice", Type: "person"},
		},
		Relations: []RelationResult{
			{Source: "Alice", Relation: "lives_in", Target: "Paris"},
		},
		Tokens: 5,
	}

	var result struct {
		ResolvedText string           `json:"resolved_text"`
		Entities     []EntityResult   `json:"entities"`
		Relations    []RelationResult `json:"relations"`
	}
	_, err := fc.Complete(context.Background(), "system", "user content", "entity_extraction", nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 1 {
		t.Fatalf("got %d entities, want 1", len(result.Entities))
	}
	if result.Entities[0].Name != "Alice" {
		t.Errorf("entity name = %q, want Alice", result.Entities[0].Name)
	}
	if len(result.Relations) != 1 {
		t.Fatalf("got %d relations, want 1", len(result.Relations))
	}
	if result.Relations[0].Target != "Paris" {
		t.Errorf("relation target = %q, want Paris", result.Relations[0].Target)
	}
}

func TestFakeCompleter_CompleteWithCorrection(t *testing.T) {
	fc := &FakeCompleter{
		Entities: []EntityResult{
			{Name: "Bob", Type: "person"},
		},
		Tokens: 7,
	}

	var result struct {
		ResolvedText string         `json:"resolved_text"`
		Entities     []EntityResult `json:"entities"`
		Relations    []any          `json:"relations"`
	}
	tokens, err := fc.CompleteWithCorrection(
		context.Background(), "system", "user", "{}", "fix it",
		"entity_extraction", nil, &result,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 7 {
		t.Errorf("tokens = %d, want 7", tokens)
	}
	if len(result.Entities) != 1 || result.Entities[0].Name != "Bob" {
		t.Errorf("unexpected entities: %+v", result.Entities)
	}
}

func TestFakeCompleter_Profile(t *testing.T) {
	fc := &FakeCompleter{
		Tokens: 3,
	}

	var result struct {
		Profile string `json:"profile"`
	}
	_, err := fc.Complete(context.Background(), "system", "user", "profile", nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "fake profile" {
		t.Errorf("profile = %q, want %q", result.Profile, "fake profile")
	}
}

func TestFakeCompleter_UnknownSchema(t *testing.T) {
	fc := &FakeCompleter{}

	var result json.RawMessage
	_, err := fc.Complete(context.Background(), "system", "user", "unknown_schema", nil, &result)
	if err == nil {
		t.Fatal("expected error for unknown schema")
	}
}

func TestFakeEmbedder_Deterministic(t *testing.T) {
	fe := &FakeEmbedder{Dim: 8}

	ctx := context.Background()
	v1, err := fe.Embed(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := fe.Embed(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}

	if len(v1) != 8 {
		t.Fatalf("dimension = %d, want 8", len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("not deterministic at index %d: %f != %f", i, v1[i], v2[i])
		}
	}
}

func TestFakeEmbedder_DifferentInputs(t *testing.T) {
	fe := &FakeEmbedder{Dim: 16}

	ctx := context.Background()
	v1, _ := fe.Embed(ctx, "hello")
	v2, _ := fe.Embed(ctx, "world")

	same := true
	for i := range v1 {
		if v1[i] != v2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different inputs should produce different vectors")
	}
}

func TestFakeEmbedder_BatchConsistency(t *testing.T) {
	fe := &FakeEmbedder{Dim: 8}

	ctx := context.Background()
	single, _ := fe.Embed(ctx, "test")
	batch, err := fe.EmbedBatch(ctx, []string{"test", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch len = %d, want 2", len(batch))
	}
	for i := range single {
		if single[i] != batch[0][i] {
			t.Fatalf("batch[0] differs from single at index %d", i)
		}
	}
}

func TestFakeEmbedder_Normalized(t *testing.T) {
	fe := &FakeEmbedder{Dim: 32}

	v, _ := fe.Embed(context.Background(), "normalize me")
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	// Should be approximately unit length (tolerance for float32)
	if sumSq < 0.99 || sumSq > 1.01 {
		t.Errorf("vector not unit-normalized: sum of squares = %f", sumSq)
	}
}

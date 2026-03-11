package faktory_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	faktory "github.com/sderosiaux/faktory"
	"github.com/sderosiaux/faktory/faktorytest"
)

func newQualifierMemory(t *testing.T, fc *faktorytest.FakeCompleter, enableQualifiers bool) *faktory.Memory {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	m, err := faktory.New(faktory.Config{
		DBPath:           dbPath,
		Completer:        fc,
		TextEmbedder:     &faktorytest.FakeEmbedder{Dim: 8},
		EmbedDimension:   8,
		DisableGraph:     true,
		EnableQualifiers: enableQualifiers,
	})
	if err != nil {
		t.Fatalf("new memory: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestQualifiers_DisabledByDefault(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
	}
	m := newQualifierMemory(t, fc, false)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like Go"}}
	result, err := m.Add(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(result.Added) == 0 {
		t.Fatal("expected at least one added fact")
	}

	f, err := m.Get(ctx, result.Added[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if f.Source != "" {
		t.Errorf("expected empty source when qualifiers disabled, got %q", f.Source)
	}
	if f.Confidence != 0 {
		t.Errorf("expected 0 confidence when qualifiers disabled, got %d", f.Confidence)
	}

	// Verify the prompt does NOT contain qualifier instructions
	prompt := fc.GetSystemPrompt("fact_extraction")
	if strings.Contains(prompt, "confidence") {
		t.Error("qualifier instructions should not be in prompt when EnableQualifiers=false")
	}
}

func TestQualifiers_ExtractedWhenEnabled(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{
			Text:       "Lives in Lyon",
			Importance: 4,
			Source:     "user",
			Confidence: 5,
		}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Lives in Lyon", Event: "ADD"}},
	}
	m := newQualifierMemory(t, fc, true)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I live in Lyon"}}
	result, err := m.Add(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(result.Added) == 0 {
		t.Fatal("expected at least one added fact")
	}

	f, err := m.Get(ctx, result.Added[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if f.Source != "user" {
		t.Errorf("expected source=user, got %q", f.Source)
	}
	if f.Confidence != 5 {
		t.Errorf("expected confidence=5, got %d", f.Confidence)
	}

	// Verify the prompt contains qualifier instructions
	prompt := fc.GetSystemPrompt("fact_extraction")
	if !strings.Contains(prompt, "confidence") {
		t.Error("qualifier instructions should be in prompt when EnableQualifiers=true")
	}
}

func TestQualifiers_ConfidenceFilter(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "High confidence fact", Importance: 3, Confidence: 5, Source: "user"},
			{Text: "Low confidence fact", Importance: 3, Confidence: 2, Source: "inferred"},
		},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "High confidence fact", Event: "ADD"},
			{ID: "1", Text: "Low confidence fact", Event: "ADD"},
		},
	}
	m := newQualifierMemory(t, fc, true)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "test"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Recall with MinConfidence=4 should exclude the low-confidence fact
	recall, err := m.Recall(ctx, "fact", "u1", &faktory.RecallOptions{MinConfidence: 4})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, f := range recall.Facts {
		if f.Confidence < 4 {
			t.Errorf("fact %q has confidence %d, should be filtered (min=4)", f.Text, f.Confidence)
		}
	}
}

func TestQualifiers_SummaryIncludesAnnotations(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{
			Text:       "Uses Vim",
			Importance: 3,
			Source:     "user",
			Confidence: 4,
		}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Uses Vim", Event: "ADD"}},
	}
	m := newQualifierMemory(t, fc, true)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I use Vim"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	recall, err := m.Recall(ctx, "Vim", "u1", nil)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(recall.Summary, "confidence: 4") {
		t.Errorf("summary should contain confidence annotation, got: %s", recall.Summary)
	}
	if !strings.Contains(recall.Summary, "source: user") {
		t.Errorf("summary should contain source annotation, got: %s", recall.Summary)
	}
}

func TestQualifiers_BackwardCompatible(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "Likes pizza", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Likes pizza", Event: "ADD"}},
	}
	m := newQualifierMemory(t, fc, false)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like pizza"}}
	result, err := m.Add(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(result.Added) == 0 {
		t.Fatal("expected at least one added fact")
	}

	// Summary should NOT have qualifier annotations when confidence=0 and source=""
	recall, err := m.Recall(ctx, "pizza", "u1", nil)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if strings.Contains(recall.Summary, "confidence:") {
		t.Errorf("summary should not have qualifier annotations when disabled, got: %s", recall.Summary)
	}
}

func TestQualifiers_PartialExtraction(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{
			{Text: "Has qualifiers", Importance: 3, Source: "user", Confidence: 4},
			{Text: "No qualifiers", Importance: 3}, // Source="" Confidence=0
		},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "Has qualifiers", Event: "ADD"},
			{ID: "1", Text: "No qualifiers", Event: "ADD"},
		},
	}
	m := newQualifierMemory(t, fc, true)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "test"}}
	result, err := m.Add(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(result.Added) < 2 {
		t.Fatalf("expected 2 added facts, got %d", len(result.Added))
	}

	// Both should be stored correctly
	all, err := m.GetAll(ctx, "u1", 10)
	if err != nil {
		t.Fatalf("getall: %v", err)
	}
	var withQ, withoutQ bool
	for _, f := range all {
		if f.Source == "user" && f.Confidence == 4 {
			withQ = true
		}
		if f.Source == "" && f.Confidence == 0 {
			withoutQ = true
		}
	}
	if !withQ {
		t.Error("expected fact with qualifiers")
	}
	if !withoutQ {
		t.Error("expected fact without qualifiers")
	}
}

func TestQualifiers_ConfidenceClamped(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{
			Text:       "Bad confidence",
			Importance: 3,
			Confidence: 99, // out of range
		}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Bad confidence", Event: "ADD"}},
	}
	m := newQualifierMemory(t, fc, true)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "test"}}
	result, err := m.Add(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(result.Added) == 0 {
		t.Fatal("expected at least one added fact")
	}

	f, err := m.Get(ctx, result.Added[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Confidence 99 should be clamped to 0 (invalid → default)
	if f.Confidence != 0 {
		t.Errorf("expected confidence clamped to 0, got %d", f.Confidence)
	}
}

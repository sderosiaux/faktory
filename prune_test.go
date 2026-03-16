package faktory_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	faktory "github.com/sderosiaux/faktory"
	"github.com/sderosiaux/faktory/faktorytest"
)

func newPruneTestMemory(t *testing.T, fc *faktorytest.FakeCompleter, cfg ...faktory.Config) *faktory.Memory {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c := faktory.Config{
		DBPath:         dbPath,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
		EmbedDimension: 8,
		DisableGraph:   true,
	}
	if len(cfg) > 0 {
		c.ConsolidateThreshold = cfg[0].ConsolidateThreshold
	}
	m, err := faktory.New(c)
	if err != nil {
		t.Fatalf("new memory: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}


func TestPrune_RequiresUserID(t *testing.T) {
	fc := &faktorytest.FakeCompleter{}
	m := newPruneTestMemory(t, fc)

	_, err := m.Prune(context.Background(), "", faktory.PruneOptions{})
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
}

func TestPrune_DryRunDoesNotDelete(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "trivial thing", Importance: 1}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "trivial thing", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "trivial thing"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{
		MinImportance: 1,
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("prune dry run: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 fact in dry run, got %d", result.Count)
	}

	// Verify fact still exists
	all, err := m.GetAll(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("getall: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 fact after dry run, got %d", len(all))
	}
}

func TestPrune_DeletesMatchingFacts(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "low importance fact", Importance: 1}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "low importance fact", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "low importance fact"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Add a high-importance fact
	fc.Facts = []faktorytest.FactResult{{Text: "critical identity info", Importance: 5}}
	fc.Reconcile = []faktorytest.ReconcileAction{{ID: "0", Text: "critical identity info", Event: "ADD"}}
	msgs2 := []faktory.Message{{Role: "user", Content: "critical identity info"}}
	if _, err := m.Add(ctx, msgs2, "u1"); err != nil {
		t.Fatalf("add2: %v", err)
	}

	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{
		MinImportance: 2, // prune facts with importance <= 2
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 pruned fact, got %d", result.Count)
	}
	if result.Count > 0 && result.Pruned[0].Text != "low importance fact" {
		t.Errorf("expected pruned fact to be 'low importance fact', got %q", result.Pruned[0].Text)
	}

	// Verify only the high-importance fact remains
	all, err := m.GetAll(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("getall: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 fact remaining, got %d", len(all))
	}
	if len(all) > 0 && all[0].Text != "critical identity info" {
		t.Errorf("remaining fact should be critical, got %q", all[0].Text)
	}
}

func TestPrune_NoMatchReturnsEmpty(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "important stuff", Importance: 5}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "important stuff", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "important stuff"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{
		MinImportance: 1, // only prune importance <= 1, but our fact is 5
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 pruned facts, got %d", result.Count)
	}
}

func TestPrune_AccessCountFilter(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "never accessed", Importance: 2}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "never accessed", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "never accessed"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Search to bump access count on this fact
	_, _ = m.Search(ctx, "never accessed", "u1", 5)

	// Prune with MaxAccessCount -1 (exactly 0 accesses) — should NOT match since we searched
	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{
		MaxAccessCount: -1,
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 facts (accessed once), got %d", result.Count)
	}
}

func TestPrune_NamespaceIsolation(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "work fact", Importance: 1}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "work fact", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	// Add to "work" namespace
	msgs := []faktory.Message{{Role: "user", Content: "work fact"}}
	if _, err := m.Add(ctx, msgs, "u1", faktory.WithNamespace("work")); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Prune default namespace — should find nothing
	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{MinImportance: 5})
	if err != nil {
		t.Fatalf("prune default: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 in default namespace, got %d", result.Count)
	}

	// Prune work namespace — should find the fact
	result, err = m.Prune(ctx, "u1", faktory.PruneOptions{
		MinImportance: 1,
		DryRun:        true,
	}, faktory.WithNamespace("work"))
	if err != nil {
		t.Fatalf("prune work: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 in work namespace, got %d", result.Count)
	}
}

func TestPrune_MaxAge(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "old fact", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "old fact", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "old fact"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Prune with MaxAge of 1 hour — fact was just created, should NOT match
	result, err := m.Prune(ctx, "u1", faktory.PruneOptions{
		MaxAge: 1 * time.Hour,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 (fact is fresh), got %d", result.Count)
	}
}

func TestAutoConsolidate_TriggersAboveThreshold(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "fact one", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "fact one", Event: "ADD"}},
	}
	m := newPruneTestMemory(t, fc, faktory.Config{ConsolidateThreshold: 1})
	ctx := context.Background()

	// Add first fact — should trigger auto-consolidation (threshold = 1)
	msgs := []faktory.Message{{Role: "user", Content: "fact one"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Give the goroutine a moment to fire
	time.Sleep(50 * time.Millisecond)

	if fc.GetCallCount("session_summary") < 1 {
		t.Errorf("expected auto-consolidation to trigger session_summary, got %d calls", fc.GetCallCount("session_summary"))
	}
}

func TestAutoConsolidate_DisabledByDefault(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "fact one", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "fact one", Event: "ADD"}},
	}
	// ConsolidateThreshold = 0 (default, disabled)
	m := newPruneTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "fact one"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if fc.GetCallCount("session_summary") != 0 {
		t.Errorf("expected no auto-consolidation when threshold is 0, got %d calls", fc.GetCallCount("session_summary"))
	}
}

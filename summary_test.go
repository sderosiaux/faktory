package faktory_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	faktory "github.com/sderosiaux/faktory"
	"github.com/sderosiaux/faktory/faktorytest"
)

func newSummaryTestMemory(t *testing.T, fc *faktorytest.FakeCompleter) *faktory.Memory {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	m, err := faktory.New(faktory.Config{
		DBPath:         dbPath,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
		EmbedDimension: 8,
		DisableGraph:   true,
	})
	if err != nil {
		t.Fatalf("new memory: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestSummarize_StoresSessionSummary(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:          []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile:      []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
		SessionSummary: "User discussed their love of Go programming",
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{
		{Role: "user", Content: "I really like Go"},
		{Role: "assistant", Content: "Go is great!"},
	}

	err := m.Summarize(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if fc.GetCallCount("session_summary") != 1 {
		t.Errorf("expected 1 session_summary call, got %d", fc.GetCallCount("session_summary"))
	}
}

func TestSummarize_EmptyMessagesNoOp(t *testing.T) {
	fc := &faktorytest.FakeCompleter{}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	err := m.Summarize(ctx, nil, "u1")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if fc.GetCallCount("session_summary") != 0 {
		t.Errorf("expected 0 calls, got %d", fc.GetCallCount("session_summary"))
	}
}

func TestSummarize_RequiresUserID(t *testing.T) {
	fc := &faktorytest.FakeCompleter{}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	err := m.Summarize(ctx, []faktory.Message{{Role: "user", Content: "hi"}}, "")
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
}

func TestSummarize_SummaryNotInGetAll(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like Go"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := m.Summarize(ctx, msgs, "u1"); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	all, err := m.GetAll(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("getall: %v", err)
	}
	for _, f := range all {
		if f.IsSummary {
			t.Error("GetAll returned a summary fact")
		}
	}
	if len(all) != 1 {
		t.Errorf("expected 1 regular fact, got %d", len(all))
	}
}

func TestSummarize_SummaryNotInSearch(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like Go"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Summarize(ctx, msgs, "u1"); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	results, err := m.Search(ctx, "Go", "u1", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, f := range results {
		if f.IsSummary {
			t.Error("Search returned a summary fact")
		}
	}
}

func TestSummarize_SummaryInRecall(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:          []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile:      []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
		SessionSummary: "User discussed Go programming",
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like Go"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Summarize(ctx, msgs, "u1"); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	recall, err := m.Recall(ctx, "Go", "u1", nil)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(recall.Summary, "Session summaries:") {
		t.Errorf("recall summary should contain Session summaries section, got: %s", recall.Summary)
	}
	if !strings.Contains(recall.Summary, "User discussed Go programming") {
		t.Errorf("recall summary should contain the summary text, got: %s", recall.Summary)
	}
}

func TestSummarize_CountFactsExcludesSummaries(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "Likes Go", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "Likes Go", Event: "ADD"}},
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "I like Go"}}
	if _, err := m.Add(ctx, msgs, "u1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := m.Summarize(ctx, msgs, "u1"); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	result, err := m.Add(ctx, []faktory.Message{{Role: "user", Content: "something new"}}, "u1")
	if err != nil {
		t.Fatalf("add2: %v", err)
	}
	if result.TotalFacts < 1 {
		t.Errorf("expected at least 1 total fact, got %d", result.TotalFacts)
	}
}

func TestSummarize_WithNamespace(t *testing.T) {
	fc := &faktorytest.FakeCompleter{
		SessionSummary: "Work discussion",
	}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "work stuff"}}
	err := m.Summarize(ctx, msgs, "u1", faktory.WithNamespace("work"))
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	recall, err := m.Recall(ctx, "work", "u1", &faktory.RecallOptions{Namespace: "work"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(recall.Summary, "Work discussion") {
		t.Errorf("expected summary in work namespace, got: %s", recall.Summary)
	}

	recall2, err := m.Recall(ctx, "work", "u1", nil)
	if err != nil {
		t.Fatalf("recall default: %v", err)
	}
	if strings.Contains(recall2.Summary, "Work discussion") {
		t.Errorf("summary should not appear in default namespace")
	}
}

func TestSummarize_FakeCompleterBackwardCompatible(t *testing.T) {
	fc := &faktorytest.FakeCompleter{}
	m := newSummaryTestMemory(t, fc)
	ctx := context.Background()

	msgs := []faktory.Message{{Role: "user", Content: "hello"}}
	err := m.Summarize(ctx, msgs, "u1")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if fc.GetCallCount("session_summary") != 1 {
		t.Errorf("expected 1 call, got %d", fc.GetCallCount("session_summary"))
	}
}

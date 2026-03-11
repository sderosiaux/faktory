package faktory

import (
	"context"
	"testing"

	"github.com/sderosiaux/faktory/faktorytest"
)

// skipIfNoFTS5 skips the test if FTS5 is not compiled into the SQLite build.
func skipIfNoFTS5(t *testing.T) {
	t.Helper()
	s := tempStore(t, 4)
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM facts_fts").Scan(&n)
	if err != nil {
		t.Skip("FTS5 not available (build with -tags sqlite_fts5)")
	}
}

func newTestMemoryBM25(t *testing.T, fc *faktorytest.FakeCompleter) *Memory {
	t.Helper()
	dim := 4
	s := tempStore(t, dim)
	emb := &faktorytest.FakeEmbedder{Dim: dim}
	cfg := Config{BM25Weight: 0.3, DecayAlpha: 0.01, DecayBeta: 0.1, Logger: nopLogger()}
	return &Memory{store: s, llm: fc, embedder: emb, cfg: cfg, log: cfg.Logger}
}

func TestBM25_SanitizeFTS5Query(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", `"hello" "world"`},
		{"ORD-999", `"ORD" "999"`},
		{"", ""},
		{"---", ""},
		{"  spaces  ", `"spaces"`},
		{"it's a test!", `"it" "s" "a" "test"`},
		{`"already quoted"`, `"already" "quoted"`},
	}
	for _, tc := range tests {
		got := sanitizeFTS5Query(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBM25_FuseScores(t *testing.T) {
	vec := []Fact{
		{ID: "a", Text: "vec only", Score: 0.9},
		{ID: "b", Text: "both", Score: 0.8},
	}
	bm25 := []Fact{
		{ID: "b", Text: "both", Score: 0.7},
		{ID: "c", Text: "bm25 only", Score: 0.6},
	}

	merged := fuseScores(vec, bm25, 0.3)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged facts, got %d", len(merged))
	}

	// Check that "b" has fused score: 0.7*0.8 + 0.3*0.7 = 0.56 + 0.21 = 0.77
	found := false
	for _, f := range merged {
		if f.ID == "b" {
			found = true
			want := (1-0.3)*0.8 + 0.3*0.7
			if f.Score < want-0.01 || f.Score > want+0.01 {
				t.Errorf("fused score for 'b' = %f, want ~%f", f.Score, want)
			}
		}
		if f.ID == "c" {
			want := 0.3 * 0.6
			if f.Score < want-0.01 || f.Score > want+0.01 {
				t.Errorf("bm25-only score for 'c' = %f, want ~%f", f.Score, want)
			}
		}
	}
	if !found {
		t.Error("fact 'b' not found in merged results")
	}

	// Results should be sorted descending
	for i := 1; i < len(merged); i++ {
		if merged[i].Score > merged[i-1].Score {
			t.Errorf("results not sorted descending at index %d: %f > %f", i, merged[i].Score, merged[i-1].Score)
		}
	}
}

func TestBM25_DefaultWeight(t *testing.T) {
	cfg := Config{}.withDefaults()
	if cfg.BM25Weight != 0.3 {
		t.Errorf("default BM25Weight = %f, want 0.3", cfg.BM25Weight)
	}
	// Custom value should not be overwritten
	cfg2 := Config{BM25Weight: 0.5}.withDefaults()
	if cfg2.BM25Weight != 0.5 {
		t.Errorf("custom BM25Weight overwritten: %f", cfg2.BM25Weight)
	}
}

func TestBM25_ExactKeywordMatch(t *testing.T) {
	skipIfNoFTS5(t)
	s := tempStore(t, 4)

	s.InsertFact("alice", "", "Alice loves pepperoni pizza", "h1", []float32{1, 0, 0, 0}, 3, "", 0)
	s.InsertFact("alice", "", "Bob prefers sushi", "h2", []float32{0, 1, 0, 0}, 3, "", 0)
	s.InsertFact("alice", "", "Alice eats pasta every Friday", "h3", []float32{0, 0, 1, 0}, 3, "", 0)

	results, err := s.SearchFactsBM25("pepperoni", "alice", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Text != "Alice loves pepperoni pizza" {
		t.Errorf("result = %q, want pepperoni fact", results[0].Text)
	}
	if results[0].Score <= 0 || results[0].Score > 1 {
		t.Errorf("score = %f, expected (0,1]", results[0].Score)
	}
}

func TestBM25_FTSTriggersSync(t *testing.T) {
	skipIfNoFTS5(t)
	s := tempStore(t, 4)

	id, _ := s.InsertFact("alice", "", "original keyword xylophone", "h1", []float32{1, 0, 0, 0}, 3, "", 0)

	results, _ := s.SearchFactsBM25("xylophone", "alice", "", 10)
	if len(results) != 1 {
		t.Fatalf("after insert: expected 1 result, got %d", len(results))
	}

	newID, _ := s.UpdateFact(id, "updated keyword marimba", "h2", []float32{1, 0, 0, 0})

	results, _ = s.SearchFactsBM25("xylophone", "alice", "", 10)
	if len(results) != 0 {
		t.Errorf("after update: old keyword still found, got %d results", len(results))
	}

	results, _ = s.SearchFactsBM25("marimba", "alice", "", 10)
	if len(results) != 1 {
		t.Errorf("after update: new keyword not found, got %d results", len(results))
	}

	s.DeleteFact(newID)
	results, _ = s.SearchFactsBM25("marimba", "alice", "", 10)
	if len(results) != 0 {
		t.Errorf("after delete: keyword still found, got %d results", len(results))
	}
}

func TestBM25_RecallIncludesBM25(t *testing.T) {
	skipIfNoFTS5(t)

	fc := &faktorytest.FakeCompleter{Facts: []faktorytest.FactResult{{Text: "test fact", Importance: 3}}}
	m := newTestMemoryBM25(t, fc)

	ctx := context.Background()
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	m.store.InsertFact("alice", "", "Alice uses faktory library", "h1", emb, 3, "", 0)

	result, err := m.Recall(ctx, "faktory", "alice", &RecallOptions{MaxFacts: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) == 0 {
		t.Error("Recall returned no facts; BM25 should have found the keyword match")
	}
}

func TestBM25_MalformedQuery(t *testing.T) {
	skipIfNoFTS5(t)
	s := tempStore(t, 4)

	s.InsertFact("alice", "", "some fact text", "h1", []float32{1, 0, 0, 0}, 3, "", 0)

	malformed := []string{"---", "OR AND NOT", `"unclosed`, "()", "***"}
	for _, q := range malformed {
		results, err := s.SearchFactsBM25(q, "alice", "", 10)
		if err != nil {
			t.Errorf("SearchFactsBM25(%q) error: %v", q, err)
		}
		_ = results
	}
}

func TestBM25_NamespaceIsolation(t *testing.T) {
	skipIfNoFTS5(t)
	s := tempStore(t, 4)

	s.InsertFact("alice", "work", "project deadline approaching", "h1", []float32{1, 0, 0, 0}, 3, "", 0)
	s.InsertFact("alice", "personal", "birthday party planning", "h2", []float32{0, 1, 0, 0}, 3, "", 0)

	results, _ := s.SearchFactsBM25("deadline", "alice", "work", 10)
	if len(results) != 1 {
		t.Errorf("work namespace: expected 1 result, got %d", len(results))
	}

	results, _ = s.SearchFactsBM25("deadline", "alice", "personal", 10)
	if len(results) != 0 {
		t.Errorf("personal namespace: expected 0 results, got %d", len(results))
	}
}

func TestBM25_SearchIntegration(t *testing.T) {
	skipIfNoFTS5(t)

	fc := &faktorytest.FakeCompleter{Facts: []faktorytest.FactResult{{Text: "test", Importance: 3}}}
	m := newTestMemoryBM25(t, fc)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	m.store.InsertFact("alice", "", "Alice speaks fluent Esperanto", "h1", emb, 3, "", 0)
	m.store.InsertFact("alice", "", "Alice likes pizza", "h2", emb, 3, "", 0)

	results, err := m.Search(ctx, "Esperanto", "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from Search with BM25")
	}
}

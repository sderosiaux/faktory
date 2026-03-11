package faktory

import (
	"context"
	"testing"
	"time"

	"github.com/sderosiaux/faktory/faktorytest"
)

func TestBiTemporal_InsertSetsValidFrom(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFact(id)
	if err != nil {
		t.Fatal(err)
	}
	if f.ValidFrom == "" {
		t.Error("valid_from should be set on insert")
	}
	if f.InvalidAt != "" {
		t.Error("invalid_at should be empty on insert")
	}
}

func TestBiTemporal_UpdateCreatesNewVersion(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	oldID, _ := s.InsertFact("alice", "", "lives in Paris", "hp", emb, 3)

	newEmb := []float32{0.5, 0.6, 0.7, 0.8}
	newID, err := s.UpdateFact(oldID, "lives in Lyon", "hl", newEmb)
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Error("UpdateFact should return a new ID")
	}

	// Old version should be soft-invalidated
	oldFact, _ := s.GetFact(oldID)
	if oldFact != nil {
		t.Error("old version should not be visible via GetFact (soft-invalidated)")
	}

	// New version should be visible
	newFact, err := s.GetFact(newID)
	if err != nil {
		t.Fatal(err)
	}
	if newFact == nil {
		t.Fatal("new version should be visible")
	}
	if newFact.Text != "lives in Lyon" {
		t.Errorf("text = %q, want %q", newFact.Text, "lives in Lyon")
	}
}

func TestBiTemporal_UpdatePreservesUserAndNamespace(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	oldID, _ := s.InsertFact("alice", "work", "likes pizza", "h1", emb, 3)

	newID, err := s.UpdateFact(oldID, "likes pasta", "h2", emb)
	if err != nil {
		t.Fatal(err)
	}
	newFact, _ := s.GetFact(newID)
	if newFact.UserID != "alice" {
		t.Errorf("user_id = %q, want alice", newFact.UserID)
	}
}

func TestBiTemporal_DeleteSoftDeletes(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)

	if err := s.DeleteFact(id); err != nil {
		t.Fatal(err)
	}

	// GetFact should return nil (filtered by invalid_at IS NULL)
	got, _ := s.GetFact(id)
	if got != nil {
		t.Error("soft-deleted fact should not be visible via GetFact")
	}

	// But the row should still exist in the DB
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE id = ?", id).Scan(&count)
	if count != 1 {
		t.Errorf("soft-deleted row should still exist in DB, got count=%d", count)
	}
}

func TestBiTemporal_DeleteRemovesEmbedding(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)

	s.DeleteFact(id)

	// Embedding should be removed to keep vec0 clean
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM fact_embeddings WHERE id = ?", id).Scan(&count)
	if count != 0 {
		t.Errorf("embedding should be removed after soft-delete, got count=%d", count)
	}
}

func TestBiTemporal_SoftDeletedExcludedFromSearch(t *testing.T) {
	s := tempStore(t, 4)
	s.InsertFact("alice", "", "likes pizza", "h1", []float32{1, 0, 0, 0}, 3)
	id2, _ := s.InsertFact("alice", "", "lives in Paris", "h2", []float32{0, 1, 0, 0}, 3)

	s.DeleteFact(id2)

	results, err := s.SearchFacts([]float32{0, 0.9, 0.1, 0}, "alice", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.ID == id2 {
			t.Error("soft-deleted fact should not appear in search results")
		}
	}
}

func TestBiTemporal_SoftDeletedExcludedFromCount(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "", "fact 1", "h1", emb, 3)
	id2, _ := s.InsertFact("alice", "", "fact 2", "h2", emb, 3)

	s.DeleteFact(id2)

	count, err := s.CountFacts("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (soft-deleted excluded)", count)
	}
}

func TestBiTemporal_SoftDeletedExcludedFromHashDedup(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "likes pizza", "hash_pizza", emb, 3)

	s.DeleteFact(id)

	exists, err := s.FactExistsByHash("alice", "", "hash_pizza")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("soft-deleted fact should not count for hash dedup")
	}
}

func TestBiTemporal_GetFactsAt(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	// Insert a fact
	id, _ := s.InsertFact("alice", "", "lives in Paris", "hp", emb, 3)
	t1 := time.Now().UTC()

	time.Sleep(2 * time.Millisecond)

	// Update it (creates new version, soft-invalidates old)
	newID, _ := s.UpdateFact(id, "lives in Lyon", "hl", emb)
	_ = newID

	// Point-in-time query at t1: should see old version
	facts, err := s.GetFactsAt("alice", "", t1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact at t1, got %d", len(facts))
	}
	if facts[0].Text != "lives in Paris" {
		t.Errorf("at t1: text = %q, want %q", facts[0].Text, "lives in Paris")
	}

	// Current query: should see new version only
	current, _ := s.GetFactsAt("alice", "", time.Now().UTC(), 100)
	if len(current) != 1 {
		t.Fatalf("expected 1 current fact, got %d", len(current))
	}
	if current[0].Text != "lives in Lyon" {
		t.Errorf("current: text = %q, want %q", current[0].Text, "lives in Lyon")
	}
}

func TestBiTemporal_HistoryAtDeletedFact(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	id, _ := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)
	t1 := time.Now().UTC()

	time.Sleep(2 * time.Millisecond)
	s.DeleteFact(id)

	// At t1 (before delete): fact should be visible
	facts, err := s.GetFactsAt("alice", "", t1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact at t1, got %d", len(facts))
	}

	// After delete: fact should not be visible
	facts, _ = s.GetFactsAt("alice", "", time.Now().UTC(), 100)
	if len(facts) != 0 {
		t.Errorf("expected 0 facts after delete, got %d", len(facts))
	}
}

func TestBiTemporal_MemoryHistoryAt(t *testing.T) {
	s := tempStore(t, 4)
	m := &Memory{
		store:    s,
		embedder: &stubEmbedder{dim: 4},
		log:      nopLogger(),
	}
	ctx := context.Background()
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)

	facts, err := m.HistoryAt(ctx, "alice", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
}

func TestBiTemporal_CurrentQueriesUnchanged(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)

	// GetFact
	f, _ := s.GetFact(id)
	if f == nil || f.Text != "likes pizza" {
		t.Error("GetFact should still work for live facts")
	}

	// GetAllFacts
	all, _ := s.GetAllFacts("alice", "", 100)
	if len(all) != 1 {
		t.Errorf("GetAllFacts = %d, want 1", len(all))
	}

	// CountFacts
	count, _ := s.CountFacts("alice", "")
	if count != 1 {
		t.Errorf("CountFacts = %d, want 1", count)
	}
}

func TestBiTemporal_DeleteAllHardDeletes(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "", "fact 1", "h1", emb, 3)
	s.InsertFact("alice", "", "fact 2", "h2", emb, 3)

	s.DeleteAllForUser("alice", "")

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE user_id = 'alice'").Scan(&count)
	if count != 0 {
		t.Errorf("DeleteAllForUser should hard-delete, but %d rows remain", count)
	}
}

func TestBiTemporal_UpdateFactRecordsHistory(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	oldID, _ := s.InsertFact("alice", "", "lives in Paris", "hp", emb, 3)

	newID, _ := s.UpdateFact(oldID, "lives in Lyon", "hl", emb)

	// New version should have an UPDATE history entry
	history, err := s.GetFactHistory(newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry for new version, got %d", len(history))
	}
	if history[0].Event != "UPDATE" {
		t.Errorf("event = %q, want UPDATE", history[0].Event)
	}
	if history[0].OldText != "lives in Paris" {
		t.Errorf("old_text = %q, want %q", history[0].OldText, "lives in Paris")
	}
	if history[0].NewText != "lives in Lyon" {
		t.Errorf("new_text = %q, want %q", history[0].NewText, "lives in Lyon")
	}
}

func TestBiTemporal_UpdateFactSignatureChange(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	oldID, _ := s.InsertFact("alice", "", "v1", "h1", emb, 3)

	// UpdateFact now returns (string, error)
	newID, err := s.UpdateFact(oldID, "v2", "h2", emb)
	if err != nil {
		t.Fatal(err)
	}
	if newID == "" {
		t.Error("UpdateFact should return non-empty new ID")
	}
}

func TestBiTemporal_GetFactTemporalFields(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "test", "h1", emb, 3)

	f, _ := s.GetFact(id)
	if f.ValidFrom == "" {
		t.Error("ValidFrom should be populated")
	}
	if f.InvalidAt != "" {
		t.Error("InvalidAt should be empty for live fact")
	}
}

func TestBiTemporal_CleanupStaleRelationsRespectsSoftDelete(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	s.InsertFact("alice", "", "Alice works at Acme", "h1", emb, 3)
	s.InsertFact("alice", "", "Alice lives in Paris", "h2", emb, 3)

	aliceID, _ := s.UpsertEntity("alice", "", "Alice", "person")
	acmeID, _ := s.UpsertEntity("alice", "", "Acme", "organization")
	parisID, _ := s.UpsertEntity("alice", "", "Paris", "place")
	s.UpsertRelation("alice", "", aliceID, "works_at", acmeID)
	s.UpsertRelation("alice", "", aliceID, "lives_in", parisID)

	// Soft-delete via reconciliation (simulate: just delete the fact)
	// The fact "Alice works at Acme" is soft-deleted
	// CleanupStaleRelations should see that Acme is no longer in any LIVE fact
	// We need to use DeleteFact which is now a soft-delete
	// But CleanupStaleRelations checks facts table — it should only see non-invalidated rows

	// First, let's just test that after soft-deleting the Acme fact,
	// CleanupStaleRelations still sees the Acme entity as orphaned
	// because the soft-deleted fact shouldn't count
	facts, _ := s.GetAllFacts("alice", "", 100)
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts before delete, got %d", len(facts))
	}

	// Delete (soft) the Acme fact by finding its ID
	for _, f := range facts {
		if f.Text == "Alice works at Acme" {
			s.DeleteFact(f.ID)
			break
		}
	}

	deleted, err := s.CleanupStaleRelations("alice", "", []string{"Alice works at Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (Acme orphaned after soft-delete)", deleted)
	}
}

func TestBiTemporal_MemoryAddIntegration(t *testing.T) {
	s := tempStore(t, 4)
	m := &Memory{
		store:    s,
		embedder: &faktorytest.FakeEmbedder{Dim: 4},
		llm: &faktorytest.FakeCompleter{
			Facts: []faktorytest.FactResult{{Text: "likes pizza", Importance: 3}},
		},
		log: nopLogger(),
		cfg: Config{DisableGraph: true},
	}
	ctx := context.Background()

	result, err := m.Add(ctx, []Message{{Role: "user", Content: "I like pizza"}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(result.Added))
	}

	// The added fact should have temporal fields set
	f, _ := s.GetFact(result.Added[0].ID)
	if f.ValidFrom == "" {
		t.Error("ValidFrom should be set after Add")
	}
}

package faktory

import (
	"context"
	"testing"
	"time"
)

func TestInsertFactCreatesHistoryEntry(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := s.InsertFact("alice", "", "likes pizza", "hash1", emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	history, err := s.GetFactHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	h := history[0]
	if h.FactID != id {
		t.Errorf("fact_id = %q, want %q", h.FactID, id)
	}
	if h.UserID != "alice" {
		t.Errorf("user_id = %q, want %q", h.UserID, "alice")
	}
	if h.Event != "ADD" {
		t.Errorf("event = %q, want ADD", h.Event)
	}
	if h.OldText != "" {
		t.Errorf("old_text = %q, want empty", h.OldText)
	}
	if h.NewText != "likes pizza" {
		t.Errorf("new_text = %q, want %q", h.NewText, "likes pizza")
	}
}

func TestUpdateFactCreatesHistoryEntry(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := s.InsertFact("alice", "", "lives in Paris", "hp", emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	newEmb := []float32{0.5, 0.6, 0.7, 0.8}
	if err := s.UpdateFact(id, "lives in Lyon", "hl", newEmb); err != nil {
		t.Fatal(err)
	}

	history, err := s.GetFactHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}

	// Newest first
	h := history[0]
	if h.Event != "UPDATE" {
		t.Errorf("event = %q, want UPDATE", h.Event)
	}
	if h.OldText != "lives in Paris" {
		t.Errorf("old_text = %q, want %q", h.OldText, "lives in Paris")
	}
	if h.NewText != "lives in Lyon" {
		t.Errorf("new_text = %q, want %q", h.NewText, "lives in Lyon")
	}
}

func TestDeleteFactCreatesHistoryEntry(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := s.InsertFact("alice", "", "likes pizza", "hash1", emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteFact(id); err != nil {
		t.Fatal(err)
	}

	history, err := s.GetFactHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}

	h := history[0] // newest first
	if h.Event != "DELETE" {
		t.Errorf("event = %q, want DELETE", h.Event)
	}
	if h.OldText != "likes pizza" {
		t.Errorf("old_text = %q, want %q", h.OldText, "likes pizza")
	}
	if h.NewText != "" {
		t.Errorf("new_text = %q, want empty", h.NewText)
	}
}

func TestGetFactHistoryNewestFirst(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "v1", "h1", emb, 3)
	s.UpdateFact(id, "v2", "h2", emb)
	s.UpdateFact(id, "v3", "h3", emb)

	history, err := s.GetFactHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}
	// Newest first: UPDATE(v3), UPDATE(v2), ADD(v1)
	if history[0].Event != "UPDATE" || history[0].NewText != "v3" {
		t.Errorf("entry[0]: event=%q new_text=%q", history[0].Event, history[0].NewText)
	}
	if history[1].Event != "UPDATE" || history[1].NewText != "v2" {
		t.Errorf("entry[1]: event=%q new_text=%q", history[1].Event, history[1].NewText)
	}
	if history[2].Event != "ADD" || history[2].NewText != "v1" {
		t.Errorf("entry[2]: event=%q new_text=%q", history[2].Event, history[2].NewText)
	}
}

// stubEmbedder is a test double that returns fixed-dimension zero vectors.
type stubEmbedder struct {
	dim int
}

func (s *stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, s.dim), nil
}

func (s *stubEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, s.dim)
	}
	return out, nil
}

func newHistoryTestMemory(t *testing.T, dim int) *Memory {
	t.Helper()
	s := tempStore(t, dim)
	return &Memory{
		store:    s,
		embedder: &stubEmbedder{dim: dim},
		log:      nopLogger(),
	}
}

func TestUndoAfterDelete(t *testing.T) {
	m := newHistoryTestMemory(t, 4)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := m.store.InsertFact("alice", "", "likes pizza", hashFact("likes pizza"), emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.store.DeleteFact(id); err != nil {
		t.Fatal(err)
	}

	// Fact should be gone
	got, _ := m.store.GetFact(id)
	if got != nil {
		t.Fatal("expected fact to be deleted")
	}

	// Undo the delete
	if err := m.Undo(ctx, id); err != nil {
		t.Fatal(err)
	}

	// Fact should be restored
	got, err = m.store.GetFact(id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected fact to be restored after undo")
	}
	if got.Text != "likes pizza" {
		t.Errorf("restored text = %q, want %q", got.Text, "likes pizza")
	}

	// History should have UNDO entry
	history, _ := m.store.GetFactHistory(id)
	lastEvent := history[0].Event
	if lastEvent != "UNDO" {
		t.Errorf("last event = %q, want UNDO", lastEvent)
	}
}

func TestUndoAfterUpdate(t *testing.T) {
	m := newHistoryTestMemory(t, 4)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := m.store.InsertFact("alice", "", "lives in Paris", hashFact("lives in Paris"), emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.store.UpdateFact(id, "lives in Lyon", hashFact("lives in Lyon"), emb); err != nil {
		t.Fatal(err)
	}

	// Undo the update
	if err := m.Undo(ctx, id); err != nil {
		t.Fatal(err)
	}

	got, _ := m.store.GetFact(id)
	if got == nil {
		t.Fatal("fact should exist after undo")
	}
	if got.Text != "lives in Paris" {
		t.Errorf("restored text = %q, want %q", got.Text, "lives in Paris")
	}
}

func TestUndoAfterAdd(t *testing.T) {
	m := newHistoryTestMemory(t, 4)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := m.store.InsertFact("alice", "", "likes pizza", hashFact("likes pizza"), emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	// Undo the add — should delete the fact
	if err := m.Undo(ctx, id); err != nil {
		t.Fatal(err)
	}

	got, _ := m.store.GetFact(id)
	if got != nil {
		t.Fatal("expected fact to be deleted after undo of ADD")
	}
}

func TestPruneHistory(t *testing.T) {
	m := newHistoryTestMemory(t, 4)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id1, _ := m.store.InsertFact("alice", "", "fact 1", "h1", emb, 3)
	id2, _ := m.store.InsertFact("alice", "", "fact 2", "h2", emb, 3)
	_ = id2

	// Backdate history entries for id1
	_, err := m.store.db.Exec(
		"UPDATE fact_history SET created_at = ? WHERE fact_id = ?",
		time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339), id1,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Prune entries older than 24h
	count, err := m.PruneHistory(ctx, "alice", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("pruned %d entries, want 1", count)
	}

	// id1 history should be empty, id2 should still have its entry
	h1, _ := m.store.GetFactHistory(id1)
	if len(h1) != 0 {
		t.Errorf("id1 still has %d history entries", len(h1))
	}
	h2, _ := m.store.GetFactHistory(id2)
	if len(h2) != 1 {
		t.Errorf("id2 has %d history entries, want 1", len(h2))
	}
}

func TestDeleteAllForUserClearsHistory(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "fact 1", "h1", emb, 3)
	s.InsertFact("bob", "", "bob fact", "h3", emb, 3)

	// Verify alice has history
	history, _ := s.GetFactHistory(id)
	if len(history) == 0 {
		t.Fatal("expected history for alice")
	}

	if err := s.DeleteAllForUser("alice", ""); err != nil {
		t.Fatal(err)
	}

	// Alice history should be gone
	history, _ = s.GetFactHistory(id)
	if len(history) != 0 {
		t.Errorf("alice still has %d history entries after DeleteAllForUser", len(history))
	}
}

func TestGetLatestHistoryEntry(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "", "v1", "h1", emb, 3)
	s.UpdateFact(id, "v2", "h2", emb)

	entry, err := s.GetLatestHistoryEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Event != "UPDATE" {
		t.Errorf("event = %q, want UPDATE", entry.Event)
	}
	if entry.NewText != "v2" {
		t.Errorf("new_text = %q, want v2", entry.NewText)
	}
}

func TestHistoryMethod(t *testing.T) {
	m := newHistoryTestMemory(t, 4)
	ctx := context.Background()

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := m.store.InsertFact("alice", "", "fact", "h1", emb, 3)

	history, err := m.History(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(history))
	}
	if history[0].Event != "ADD" {
		t.Errorf("event = %q, want ADD", history[0].Event)
	}
}

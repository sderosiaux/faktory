package faktory

import (
	"os"
	"testing"
)

func tempStore(t *testing.T, dim int) *Store {
	t.Helper()
	f, err := os.CreateTemp("", "faktory-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	})

	s, err := OpenStore(path, dim)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertAndGetFact(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, err := s.InsertFact("alice", "likes pizza", "hash1", emb)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFact(id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected fact, got nil")
	}
	if got.Text != "likes pizza" {
		t.Errorf("text = %q, want %q", got.Text, "likes pizza")
	}
	if got.UserID != "alice" {
		t.Errorf("user_id = %q, want %q", got.UserID, "alice")
	}
}

func TestGetAllFacts(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "fact 1", "h1", emb)
	s.InsertFact("alice", "fact 2", "h2", emb)
	s.InsertFact("bob", "bob fact", "h3", emb)

	facts, err := s.GetAllFacts("alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Errorf("got %d facts, want 2", len(facts))
	}

	bobFacts, _ := s.GetAllFacts("bob", 100)
	if len(bobFacts) != 1 {
		t.Errorf("got %d bob facts, want 1", len(bobFacts))
	}
}

func TestDeleteFact(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "to delete", "hd", emb)

	if err := s.DeleteFact(id); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFact(id)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestUpdateFact(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	id, _ := s.InsertFact("alice", "lives in Paris", "hp", emb)

	newEmb := []float32{0.5, 0.6, 0.7, 0.8}
	if err := s.UpdateFact(id, "lives in Lyon", "hl", newEmb); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetFact(id)
	if got.Text != "lives in Lyon" {
		t.Errorf("text = %q, want %q", got.Text, "lives in Lyon")
	}
}

func TestHashDedup(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "likes pizza", "hash_pizza", emb)

	exists, err := s.FactExistsByHash("alice", "hash_pizza")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected hash to exist")
	}

	exists, _ = s.FactExistsByHash("alice", "other_hash")
	if exists {
		t.Error("expected hash to not exist")
	}

	// Different user, same hash
	exists, _ = s.FactExistsByHash("bob", "hash_pizza")
	if exists {
		t.Error("expected hash to not exist for different user")
	}
}

func TestSearchFacts(t *testing.T) {
	s := tempStore(t, 4)

	s.InsertFact("alice", "likes pizza", "h1", []float32{1, 0, 0, 0})
	s.InsertFact("alice", "lives in Paris", "h2", []float32{0, 1, 0, 0})
	s.InsertFact("bob", "bob stuff", "h3", []float32{1, 0, 0, 0})

	// Search with a vector close to "likes pizza"
	results, err := s.SearchFacts([]float32{0.9, 0.1, 0, 0}, "alice", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// First result should be "likes pizza" (closer to query)
	if results[0].Text != "likes pizza" {
		t.Errorf("first result = %q, want %q", results[0].Text, "likes pizza")
	}
}

func TestDeleteAllForUser(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "fact 1", "h1", emb)
	s.InsertFact("alice", "fact 2", "h2", emb)
	s.InsertFact("bob", "bob fact", "h3", emb)

	s.UpsertEntity("alice", "Alice", "person")
	s.UpsertEntity("alice", "Acme", "organization")
	srcID, _ := s.UpsertEntity("alice", "Alice", "person")
	tgtID, _ := s.UpsertEntity("alice", "Acme", "organization")
	s.UpsertRelation("alice", srcID, "works_at", tgtID)

	if err := s.DeleteAllForUser("alice"); err != nil {
		t.Fatal(err)
	}

	aliceFacts, _ := s.GetAllFacts("alice", 100)
	if len(aliceFacts) != 0 {
		t.Errorf("alice still has %d facts", len(aliceFacts))
	}

	bobFacts, _ := s.GetAllFacts("bob", 100)
	if len(bobFacts) != 1 {
		t.Errorf("bob has %d facts, want 1", len(bobFacts))
	}

	aliceRels, _ := s.GetAllRelations("alice", 100)
	if len(aliceRels) != 0 {
		t.Errorf("alice still has %d relations", len(aliceRels))
	}
}

func TestEntityAndRelationUpsert(t *testing.T) {
	s := tempStore(t, 4)

	id1, err := s.UpsertEntity("alice", "Alice", "person")
	if err != nil {
		t.Fatal(err)
	}

	// Upsert again should return same ID
	id2, err := s.UpsertEntity("alice", "Alice", "person")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("upsert returned different IDs: %s vs %s", id1, id2)
	}

	targetID, _ := s.UpsertEntity("alice", "Acme", "organization")

	if err := s.UpsertRelation("alice", id1, "works_at", targetID); err != nil {
		t.Fatal(err)
	}

	// Upsert same relation again should not error
	if err := s.UpsertRelation("alice", id1, "works_at", targetID); err != nil {
		t.Fatal(err)
	}

	rels, err := s.GetAllRelations("alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("got %d relations, want 1", len(rels))
	}
	if rels[0].Source != "Alice" || rels[0].Target != "Acme" || rels[0].Relation != "works_at" {
		t.Errorf("unexpected relation: %+v", rels[0])
	}
}

func TestSearchRelations(t *testing.T) {
	s := tempStore(t, 4)

	srcID, _ := s.UpsertEntity("alice", "Alice", "person")
	tgtID, _ := s.UpsertEntity("alice", "Lyon", "place")
	s.UpsertRelation("alice", srcID, "lives_in", tgtID)

	rels, err := s.SearchRelations("Alice", "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("got %d results, want 1", len(rels))
	}

	rels, _ = s.SearchRelations("Lyon", "alice", 10)
	if len(rels) != 1 {
		t.Errorf("got %d results for Lyon, want 1", len(rels))
	}

	rels, _ = s.SearchRelations("Alice", "bob", 10)
	if len(rels) != 0 {
		t.Errorf("bob should have 0 results, got %d", len(rels))
	}
}

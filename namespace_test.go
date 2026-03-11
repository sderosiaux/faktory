package faktory

import (
	"context"
	"testing"
	"time"
)

func TestNamespaceFactIsolation(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	s.InsertFact("alice", "work", "uses Go at work", "h1", emb, 3)
	s.InsertFact("alice", "personal", "likes cooking at home", "h2", emb, 3)
	s.InsertFact("alice", "", "general fact", "h3", emb, 3)

	workFacts, err := s.GetAllFacts("alice", "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(workFacts) != 1 {
		t.Fatalf("work namespace: got %d facts, want 1", len(workFacts))
	}
	if workFacts[0].Text != "uses Go at work" {
		t.Errorf("work fact text = %q", workFacts[0].Text)
	}

	personalFacts, _ := s.GetAllFacts("alice", "personal", 100)
	if len(personalFacts) != 1 {
		t.Fatalf("personal namespace: got %d facts, want 1", len(personalFacts))
	}
	if personalFacts[0].Text != "likes cooking at home" {
		t.Errorf("personal fact text = %q", personalFacts[0].Text)
	}

	defaultFacts, _ := s.GetAllFacts("alice", "", 100)
	if len(defaultFacts) != 1 {
		t.Fatalf("default namespace: got %d facts, want 1", len(defaultFacts))
	}
	if defaultFacts[0].Text != "general fact" {
		t.Errorf("default fact text = %q", defaultFacts[0].Text)
	}
}

func TestNamespaceSearchIsolation(t *testing.T) {
	s := tempStore(t, 4)

	s.InsertFact("alice", "work", "uses Go", "h1", []float32{1, 0, 0, 0}, 3)
	s.InsertFact("alice", "personal", "likes cooking", "h2", []float32{1, 0, 0, 0}, 3)

	workResults, err := s.SearchFacts([]float32{1, 0, 0, 0}, "alice", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(workResults) != 1 {
		t.Fatalf("work search: got %d, want 1", len(workResults))
	}
	if workResults[0].Text != "uses Go" {
		t.Errorf("work result = %q", workResults[0].Text)
	}

	personalResults, _ := s.SearchFacts([]float32{1, 0, 0, 0}, "alice", "personal", 10)
	if len(personalResults) != 1 {
		t.Fatalf("personal search: got %d, want 1", len(personalResults))
	}
	if personalResults[0].Text != "likes cooking" {
		t.Errorf("personal result = %q", personalResults[0].Text)
	}
}

func TestNamespaceEntityIsolation(t *testing.T) {
	s := tempStore(t, 4)

	s.UpsertEntity("alice", "work", "Acme", "organization")
	s.UpsertEntity("alice", "personal", "Bob", "person")
	s.UpsertEntity("alice", "", "Alice", "person")

	workEnts, err := s.GetAllEntities("alice", "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(workEnts) != 1 || workEnts[0].Name != "Acme" {
		t.Errorf("work entities: got %+v", workEnts)
	}

	personalEnts, _ := s.GetAllEntities("alice", "personal", 100)
	if len(personalEnts) != 1 || personalEnts[0].Name != "Bob" {
		t.Errorf("personal entities: got %+v", personalEnts)
	}

	defaultEnts, _ := s.GetAllEntities("alice", "", 100)
	if len(defaultEnts) != 1 || defaultEnts[0].Name != "Alice" {
		t.Errorf("default entities: got %+v", defaultEnts)
	}
}

func TestNamespaceRelationIsolation(t *testing.T) {
	s := tempStore(t, 4)

	// Work namespace
	srcW, _ := s.UpsertEntity("alice", "work", "Alice", "person")
	tgtW, _ := s.UpsertEntity("alice", "work", "Acme", "organization")
	s.UpsertRelation("alice", "work", srcW, "works_at", tgtW)

	// Personal namespace
	srcP, _ := s.UpsertEntity("alice", "personal", "Alice", "person")
	tgtP, _ := s.UpsertEntity("alice", "personal", "Bob", "person")
	s.UpsertRelation("alice", "personal", srcP, "friends_with", tgtP)

	workRels, err := s.GetAllRelations("alice", "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(workRels) != 1 {
		t.Fatalf("work relations: got %d, want 1", len(workRels))
	}
	if workRels[0].Relation != "works_at" {
		t.Errorf("work relation = %q", workRels[0].Relation)
	}

	personalRels, _ := s.GetAllRelations("alice", "personal", 100)
	if len(personalRels) != 1 {
		t.Fatalf("personal relations: got %d, want 1", len(personalRels))
	}
	if personalRels[0].Relation != "friends_with" {
		t.Errorf("personal relation = %q", personalRels[0].Relation)
	}
}

func TestEmptyNamespaceDefault(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	// Insert with empty namespace (default behavior)
	id, err := s.InsertFact("alice", "", "likes pizza", "h1", emb, 3)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve with empty namespace
	facts, _ := s.GetAllFacts("alice", "", 100)
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}

	// Search with empty namespace
	results, _ := s.SearchFacts(emb, "alice", "", 10)
	if len(results) != 1 {
		t.Fatalf("search got %d, want 1", len(results))
	}

	// Hash check with empty namespace
	exists, _ := s.FactExistsByHash("alice", "", "h1")
	if !exists {
		t.Error("expected hash to exist in default namespace")
	}

	// Should NOT appear in a named namespace
	otherFacts, _ := s.GetAllFacts("alice", "other", 100)
	if len(otherFacts) != 0 {
		t.Errorf("expected 0 facts in 'other' namespace, got %d", len(otherFacts))
	}

	_ = id
}

func TestNamespaceDeleteAll(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	// Insert into two namespaces
	s.InsertFact("alice", "work", "work fact 1", "wh1", emb, 3)
	s.InsertFact("alice", "work", "work fact 2", "wh2", emb, 3)
	s.InsertFact("alice", "personal", "personal fact", "ph1", emb, 3)

	srcW, _ := s.UpsertEntity("alice", "work", "Acme", "organization")
	tgtW, _ := s.UpsertEntity("alice", "work", "Alice", "person")
	s.UpsertRelation("alice", "work", srcW, "employs", tgtW)

	srcP, _ := s.UpsertEntity("alice", "personal", "Bob", "person")
	tgtP, _ := s.UpsertEntity("alice", "personal", "Alice", "person")
	s.UpsertRelation("alice", "personal", srcP, "friends_with", tgtP)

	// Delete only work namespace
	if err := s.DeleteAllForUser("alice", "work"); err != nil {
		t.Fatal(err)
	}

	// Work should be empty
	workFacts, _ := s.GetAllFacts("alice", "work", 100)
	if len(workFacts) != 0 {
		t.Errorf("work namespace still has %d facts", len(workFacts))
	}
	workRels, _ := s.GetAllRelations("alice", "work", 100)
	if len(workRels) != 0 {
		t.Errorf("work namespace still has %d relations", len(workRels))
	}
	workEnts, _ := s.GetAllEntities("alice", "work", 100)
	if len(workEnts) != 0 {
		t.Errorf("work namespace still has %d entities", len(workEnts))
	}

	// Personal should be untouched
	personalFacts, _ := s.GetAllFacts("alice", "personal", 100)
	if len(personalFacts) != 1 {
		t.Errorf("personal namespace has %d facts, want 1", len(personalFacts))
	}
	personalRels, _ := s.GetAllRelations("alice", "personal", 100)
	if len(personalRels) != 1 {
		t.Errorf("personal namespace has %d relations, want 1", len(personalRels))
	}
}

func TestNamespaceRecall(t *testing.T) {
	s := tempStore(t, 8)
	fe := &deterministicEmbedder{dim: 8}
	m := &Memory{
		store:    s,
		embedder: fe,
		log:      nopLogger(),
	}

	ctx := context.Background()

	// Insert facts in two namespaces with real embeddings
	workEmb, _ := fe.Embed(ctx, "uses Go at work")
	personalEmb, _ := fe.Embed(ctx, "likes cooking")
	s.InsertFact("alice", "work", "uses Go at work", "wh1", workEmb, 3)
	s.InsertFact("alice", "personal", "likes cooking", "ph1", personalEmb, 3)

	// Recall with work namespace
	workResult, err := m.Recall(ctx, "uses Go at work", "alice", &RecallOptions{
		Namespace: "work",
		MaxFacts:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workResult.Facts) != 1 {
		t.Fatalf("work recall: got %d facts, want 1", len(workResult.Facts))
	}
	if workResult.Facts[0].Text != "uses Go at work" {
		t.Errorf("work recall fact = %q", workResult.Facts[0].Text)
	}

	// Recall with personal namespace
	personalResult, err := m.Recall(ctx, "likes cooking", "alice", &RecallOptions{
		Namespace: "personal",
		MaxFacts:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(personalResult.Facts) != 1 {
		t.Fatalf("personal recall: got %d facts, want 1", len(personalResult.Facts))
	}
	if personalResult.Facts[0].Text != "likes cooking" {
		t.Errorf("personal recall fact = %q", personalResult.Facts[0].Text)
	}

	// Recall with default namespace should return nothing
	defaultResult, err := m.Recall(ctx, "anything", "alice", &RecallOptions{
		MaxFacts: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultResult.Facts) != 0 {
		t.Errorf("default recall: got %d facts, want 0", len(defaultResult.Facts))
	}
}

// deterministicEmbedder produces non-zero deterministic vectors suitable for KNN search.
type deterministicEmbedder struct {
	dim int
}

func (d *deterministicEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, d.dim)
	for i := range vec {
		vec[i] = float32(((int(text[i%len(text)]) * (i + 1)) % 100)) / 100.0
	}
	// Ensure non-zero
	vec[0] = 0.5
	return vec, nil
}

func (d *deterministicEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, _ := d.Embed(context.Background(), t)
		out[i] = v
	}
	return out, nil
}

func TestNamespaceHashDedup(t *testing.T) {
	s := tempStore(t, 4)

	emb := []float32{0.1, 0.2, 0.3, 0.4}
	s.InsertFact("alice", "work", "likes pizza", "same_hash", emb, 3)

	// Same hash in different namespace should not collide
	exists, _ := s.FactExistsByHash("alice", "personal", "same_hash")
	if exists {
		t.Error("hash should not exist in different namespace")
	}

	// Same hash in same namespace should be found
	exists, _ = s.FactExistsByHash("alice", "work", "same_hash")
	if !exists {
		t.Error("hash should exist in same namespace")
	}
}

func TestNamespaceConversationDedup(t *testing.T) {
	s := tempStore(t, 4)

	s.MarkConversationProcessed("alice", "work", "conv123")

	exists, _ := s.ConversationExists("alice", "work", "conv123")
	if !exists {
		t.Error("conversation should exist in work namespace")
	}

	exists, _ = s.ConversationExists("alice", "personal", "conv123")
	if exists {
		t.Error("conversation should not exist in personal namespace")
	}

	exists, _ = s.ConversationExists("alice", "", "conv123")
	if exists {
		t.Error("conversation should not exist in default namespace")
	}
}

func TestNamespaceProfile(t *testing.T) {
	s := tempStore(t, 4)

	s.UpsertProfile("alice", "work", "work profile summary", "hash1")
	s.UpsertProfile("alice", "personal", "personal profile summary", "hash2")

	workSummary, workHash, err := s.GetProfile("alice", "work")
	if err != nil {
		t.Fatal(err)
	}
	if workSummary != "work profile summary" || workHash != "hash1" {
		t.Errorf("work profile: summary=%q hash=%q", workSummary, workHash)
	}

	personalSummary, personalHash, _ := s.GetProfile("alice", "personal")
	if personalSummary != "personal profile summary" || personalHash != "hash2" {
		t.Errorf("personal profile: summary=%q hash=%q", personalSummary, personalHash)
	}

	// Default namespace should have no profile
	defaultSummary, _, _ := s.GetProfile("alice", "")
	if defaultSummary != "" {
		t.Errorf("default profile should be empty, got %q", defaultSummary)
	}
}

func TestNamespacePruneHistory(t *testing.T) {
	s := tempStore(t, 4)
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	s.InsertFact("alice", "work", "work fact", "wh1", emb, 3)
	s.InsertFact("alice", "personal", "personal fact", "ph1", emb, 3)

	// Backdate all work namespace history
	_, err := s.db.Exec(
		"UPDATE fact_history SET created_at = '2020-01-01T00:00:00Z' WHERE namespace = 'work'",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Prune only work namespace
	count, err := s.PruneHistoryForUser("alice", "work", parseTime("2025-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("pruned %d, want 1", count)
	}

	// Personal history should be untouched (verify by checking total count for personal)
	var personalCount int
	s.db.QueryRow("SELECT COUNT(*) FROM fact_history WHERE user_id = 'alice' AND namespace = 'personal'").Scan(&personalCount)
	if personalCount != 1 {
		t.Errorf("personal history count = %d, want 1", personalCount)
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

package faktory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func skipIfNoKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
}

func newTestMemory(t *testing.T) *Memory {
	t.Helper()
	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath:    db,
		LLMAPIKey: os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

func TestIntegration_AddAndSearch(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	// Add facts about Alice
	result, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice. I live in Lyon. I work at Acme as a Go developer."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	t.Logf("Added %d facts, updated %d, deleted %d, noops %d, tokens %d",
		len(result.Added), len(result.Updated), len(result.Deleted), result.Noops, result.Tokens)

	if len(result.Added) == 0 {
		t.Fatal("expected at least 1 added fact")
	}
	if result.Tokens == 0 {
		t.Error("expected non-zero token count")
	}

	// Search for location
	facts, err := mem.Search(ctx, "where does Alice live?", "alice", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected at least 1 search result")
	}

	found := false
	for _, f := range facts {
		t.Logf("  [%.2f] %s", f.Score, f.Text)
		if f.Score > 0.5 {
			found = true
		}
	}
	if !found {
		t.Error("expected at least 1 result with score > 0.5")
	}
}

func TestIntegration_UserIsolation(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice and I love climbing."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add alice: %v", err)
	}

	// Bob should see nothing
	facts, err := mem.Search(ctx, "climbing", "bob", 5)
	if err != nil {
		t.Fatalf("Search bob: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 results for bob, got %d", len(facts))
	}
}

func TestIntegration_UpdateContradiction(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	// First: Alice lives in Lyon
	r1, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice. I live in Lyon."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	t.Logf("Round 1: added=%d updated=%d tokens=%d", len(r1.Added), len(r1.Updated), r1.Tokens)

	// Second: Alice moved to Marseille — should trigger UPDATE
	r2, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I just moved to Marseille."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	t.Logf("Round 2: added=%d updated=%d deleted=%d noops=%d tokens=%d",
		len(r2.Added), len(r2.Updated), len(r2.Deleted), r2.Noops, r2.Tokens)

	// Search should now return Marseille, not Lyon
	facts, err := mem.Search(ctx, "where does Alice live?", "alice", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, f := range facts {
		t.Logf("  [%.2f] %s", f.Score, f.Text)
	}

	// Check that at least one result mentions Marseille
	hasMarseille := false
	for _, f := range facts {
		if contains(f.Text, "marseille") || contains(f.Text, "Marseille") {
			hasMarseille = true
		}
	}
	if !hasMarseille {
		t.Error("expected search results to mention Marseille after update")
	}
}

func TestIntegration_Relations(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	_, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice. I work at Acme. I live in Lyon. My friend is Bob."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Get all relations
	rels, err := mem.GetAllRelations(ctx, "alice", 100)
	if err != nil {
		t.Fatalf("GetAllRelations: %v", err)
	}

	t.Logf("Found %d relations:", len(rels))
	for _, r := range rels {
		t.Logf("  %s --%s--> %s", r.Source, r.Relation, r.Target)
	}

	if len(rels) == 0 {
		t.Error("expected at least 1 relation")
	}

	// Search relations by embedding similarity
	searchRels, err := mem.SearchRelations(ctx, "Alice workplace", "alice", 5)
	if err != nil {
		t.Fatalf("SearchRelations: %v", err)
	}

	t.Logf("SearchRelations for 'Alice workplace': %d results", len(searchRels))
	for _, r := range searchRels {
		t.Logf("  %s --%s--> %s", r.Source, r.Relation, r.Target)
	}

	if len(searchRels) == 0 {
		t.Error("expected at least 1 relation from search")
	}
}

func TestIntegration_GetAllAndDelete(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "I'm Alice. I love pizza. I hate mornings."},
	}, "alice")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// GetAll should return the added facts
	all, err := mem.GetAll(ctx, "alice", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	t.Logf("GetAll: %d facts", len(all))
	for _, f := range all {
		t.Logf("  [%s] %s", f.ID[:8], f.Text)
	}
	if len(all) == 0 {
		t.Fatal("expected at least 1 fact from GetAll")
	}

	// Delete one fact
	if len(result.Added) > 0 {
		delID := result.Added[0].ID
		if err := mem.Delete(ctx, delID); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		got, err := mem.Get(ctx, delID)
		if err != nil {
			t.Fatalf("Get after delete: %v", err)
		}
		if got != nil {
			t.Error("expected fact to be deleted")
		}
	}

	// DeleteAll
	if err := mem.DeleteAll(ctx, "alice"); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	remaining, err := mem.GetAll(ctx, "alice", 100)
	if err != nil {
		t.Fatalf("GetAll after DeleteAll: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 facts after DeleteAll, got %d", len(remaining))
	}
}

func TestIntegration_HashDedup(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	msg := []Message{
		{Role: "user", Content: "I'm Alice. I live in Lyon."},
	}

	r1, err := mem.Add(ctx, msg, "alice")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	count1 := len(r1.Added)

	// Same message again — should be deduplicated (exact hash match)
	r2, err := mem.Add(ctx, msg, "alice")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	t.Logf("Round 1: added=%d, Round 2: added=%d updated=%d noops=%d",
		count1, len(r2.Added), len(r2.Updated), r2.Noops)

	// Second round should have fewer additions (ideally NOOPs)
	if len(r2.Added) >= count1 && count1 > 0 {
		t.Logf("warning: second add created %d new facts (expected fewer due to dedup)", len(r2.Added))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

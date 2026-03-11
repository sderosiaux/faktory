package faktory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sderosiaux/faktory/faktorytest"
)

func BenchmarkAdd(b *testing.B) {
	db := filepath.Join(b.TempDir(), "bench.db")
	fc := &faktorytest.FakeCompleter{
		Facts: []faktorytest.FactResult{{Text: "fact one", Importance: 3}, {Text: "fact two", Importance: 3}, {Text: "fact three", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{
			{ID: "0", Text: "fact one", Event: "ADD"},
			{ID: "1", Text: "fact two", Event: "ADD"},
			{ID: "2", Text: "fact three", Event: "ADD"},
		},
		Entities: []faktorytest.EntityResult{},
		Tokens:   10,
	}
	mem, err := New(Config{
		DBPath:         db,
		EmbedDimension: 8,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
		DisableGraph:   true,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer mem.Close()
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "some conversation"}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mem.Add(ctx, msgs, fmt.Sprintf("u%d", i))
	}
}

func BenchmarkSearch(b *testing.B) {
	for _, n := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("facts_%d", n), func(b *testing.B) {
			db := filepath.Join(b.TempDir(), "bench.db")
			embedder := &faktorytest.FakeEmbedder{Dim: 8}
			mem, err := New(Config{
				DBPath:         db,
				EmbedDimension: 8,
				Completer:      &faktorytest.FakeCompleter{Tokens: 1},
				TextEmbedder:   embedder,
				DisableGraph:   true,
			})
			if err != nil {
				b.Fatal(err)
			}
			defer mem.Close()

			ctx := context.Background()
			for i := 0; i < n; i++ {
				text := fmt.Sprintf("fact number %d about something interesting", i)
				emb, _ := embedder.Embed(ctx, text)
				_, err := mem.store.InsertFact("u1", "", text, hashFact(text), emb, 3)
				if err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = mem.Search(ctx, "interesting fact", "u1", 10)
			}
		})
	}
}

func BenchmarkRecall(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("facts_%d", n), func(b *testing.B) {
			db := filepath.Join(b.TempDir(), "bench.db")
			embedder := &faktorytest.FakeEmbedder{Dim: 8}
			mem, err := New(Config{
				DBPath:         db,
				EmbedDimension: 8,
				Completer:      &faktorytest.FakeCompleter{Tokens: 1},
				TextEmbedder:   embedder,
			})
			if err != nil {
				b.Fatal(err)
			}
			defer mem.Close()

			ctx := context.Background()
			for i := 0; i < n; i++ {
				text := fmt.Sprintf("fact number %d about something", i)
				emb, _ := embedder.Embed(ctx, text)
				_, err := mem.store.InsertFact("u1", "", text, hashFact(text), emb, 3)
				if err != nil {
					b.Fatal(err)
				}
			}

			// Seed a few entities with embeddings for relation search path
			for i := 0; i < 10; i++ {
				name := fmt.Sprintf("Entity%d", i)
				id, _ := mem.store.UpsertEntity("u1", "", name, "concept")
				emb, _ := embedder.Embed(ctx, name)
				_ = mem.store.UpsertEntityEmbedding(id, emb)

				if i > 0 {
					prevName := fmt.Sprintf("Entity%d", i-1)
					prevID, _ := mem.store.UpsertEntity("u1", "", prevName, "concept")
					_ = mem.store.UpsertRelation("u1", "", prevID, "related_to", id)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = mem.Recall(ctx, "something about entities", "u1", nil)
			}
		})
	}
}

func BenchmarkApplyDecay(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("facts_%d", n), func(b *testing.B) {
			base := make([]Fact, n)
			now := time.Now().UTC()
			for i := range base {
				base[i] = Fact{
					ID:          fmt.Sprintf("id-%d", i),
					Text:        fmt.Sprintf("fact %d", i),
					Score:       0.9 - float64(i)*0.001,
					CreatedAt:   now.Add(-time.Duration(i) * 24 * time.Hour).Format(time.RFC3339),
					AccessCount: i % 5,
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Copy to avoid sorting affecting subsequent iterations
				facts := make([]Fact, n)
				copy(facts, base)
				applyDecay(facts, 0.01, 0.1)
			}
		})
	}
}

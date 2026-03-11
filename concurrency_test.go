package faktory

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sderosiaux/faktory/faktorytest"
)

// newConcurrencyTestMemory creates a Memory backed by a temp-dir SQLite DB
// using FakeCompleter and FakeEmbedder (dimension 8).
func newConcurrencyTestMemory(t *testing.T) *Memory {
	t.Helper()
	fc := &faktorytest.FakeCompleter{
		Facts:     []faktorytest.FactResult{{Text: "test fact", Importance: 3}},
		Reconcile: []faktorytest.ReconcileAction{{ID: "0", Text: "test fact", Event: "ADD"}},
		Entities:  []faktorytest.EntityResult{{Name: "Test", Type: "person"}},
		Relations: []faktorytest.RelationResult{},
		Tokens:    5,
	}
	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath:         db,
		EmbedDimension: 8,
		Completer:      fc,
		TextEmbedder:   &faktorytest.FakeEmbedder{Dim: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	return mem
}

func TestConcurrentAdd_SameUser(t *testing.T) {
	mem := newConcurrencyTestMemory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine sends a unique message to bypass conversation-level dedup.
			msgs := []Message{{Role: "user", Content: fmt.Sprintf("message %d", idx)}}
			_, errs[idx] = mem.Add(ctx, msgs, "u1")
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: concurrent Add() did not complete within timeout")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// Verify facts are in the store.
	facts, err := mem.GetAll(ctx, "u1", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(facts) == 0 {
		t.Error("expected facts in store after concurrent Add(), got 0")
	}
}

func TestConcurrentAdd_DifferentUsers(t *testing.T) {
	mem := newConcurrencyTestMemory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			userID := fmt.Sprintf("u%d", idx+1)
			msgs := []Message{{Role: "user", Content: fmt.Sprintf("user %d fact", idx)}}
			_, errs[idx] = mem.Add(ctx, msgs, userID)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: concurrent Add() for different users did not complete within timeout")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// Verify each user has their own facts (user isolation).
	for i := range n {
		userID := fmt.Sprintf("u%d", i+1)
		facts, err := mem.GetAll(ctx, userID, 100)
		if err != nil {
			t.Errorf("GetAll(%s): %v", userID, err)
			continue
		}
		if len(facts) == 0 {
			t.Errorf("user %s: expected facts, got 0", userID)
		}
		for _, f := range facts {
			if f.UserID != userID {
				t.Errorf("user %s: fact has UserID=%s, want %s", userID, f.UserID, userID)
			}
		}
	}
}

func TestConcurrentRecall_DuringAdd(t *testing.T) {
	mem := newConcurrencyTestMemory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Pre-seed a fact so Recall has something to find.
	fe := &faktorytest.FakeEmbedder{Dim: 8}
	emb, _ := fe.Embed(ctx, "seed fact")
	if _, err := mem.store.InsertFact("u1", "", "seed fact", hashFact("seed fact"), emb, 3, "", 0); err != nil {
		t.Fatal(err)
	}

	const addN = 5
	const recallN = 5
	var wg sync.WaitGroup
	addErrs := make([]error, addN)
	recallErrs := make([]error, recallN)

	wg.Add(addN + recallN)

	for i := range addN {
		go func(idx int) {
			defer wg.Done()
			msgs := []Message{{Role: "user", Content: fmt.Sprintf("concurrent add %d", idx)}}
			_, addErrs[idx] = mem.Add(ctx, msgs, "u1")
		}(i)
	}

	for i := range recallN {
		go func(idx int) {
			defer wg.Done()
			_, recallErrs[idx] = mem.Recall(ctx, "seed", "u1", nil)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: concurrent Add()+Recall() did not complete within timeout")
	}

	for i, err := range addErrs {
		if err != nil {
			t.Errorf("Add goroutine %d: %v", i, err)
		}
	}
	for i, err := range recallErrs {
		if err != nil {
			t.Errorf("Recall goroutine %d: %v", i, err)
		}
	}
}

func TestConcurrentBumpAccess(t *testing.T) {
	mem := newConcurrencyTestMemory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Insert a fact to search for.
	fe := &faktorytest.FakeEmbedder{Dim: 8}
	emb, _ := fe.Embed(ctx, "bump target")
	factID, err := mem.store.InsertFact("u1", "", "bump target", hashFact("bump target"), emb, 3, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)

	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			// Search triggers BumpAccess internally.
			_, errs[idx] = mem.Search(ctx, "bump target", "u1", 5)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: concurrent Search()/BumpAccess did not complete within timeout")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	// Verify access_count was bumped at least once.
	f, err := mem.Get(ctx, factID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f == nil {
		t.Fatal("fact not found after concurrent BumpAccess")
	}
	if f.AccessCount < 1 {
		t.Errorf("access_count = %d, want >= 1", f.AccessCount)
	}
}

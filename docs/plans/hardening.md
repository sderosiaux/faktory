# Hardening Plan

Post-challenge analysis. Fixes every structural weakness surfaced by the 5-probe depth analysis.

## Milestone 1: Reconciliation Context Bomb

**Problem**: `addFacts()` loads ALL similar existing facts into the reconciliation prompt. At 1000+ facts per user, the prompt exceeds context windows and the LLM can't track hundreds of integer IDs. Silent garbage output.

**Fix — Similarity gate + context cap**:

### 1a. Similarity threshold gate
In `addFacts()` (memory.go:182-195), after `SearchFacts` returns similar facts, filter out any with similarity < 0.5. If a candidate has zero similar facts above threshold, skip reconciliation entirely — just ADD it. This cuts 3 LLM calls to 2 for the common case (genuinely novel facts).

```go
// After SearchFacts, before adding to candidates:
var relevant []Fact
for _, f := range similar {
    if f.Score >= 0.5 {
        relevant = append(relevant, f)
    }
}
// If no relevant existing facts, fast-path to ADD
```

### 1b. Context cap on reconciliation
Cap the reconciliation prompt at ~30 existing facts (the LLM's reliable tracking limit). When more exist, keep only the top-30 by similarity score. Log a warning when facts are dropped.

### 1c. Chunked reconciliation for large stores
When candidate facts > 20, split into chunks of 10 and reconcile each chunk independently against the relevant existing facts. Prevents single-prompt overload.

**Tests**:
- `TestSimilarityGateSkipsReconciliation` — novel facts bypass reconciliation
- `TestReconciliationContextCap` — >30 similar facts gets capped
- `TestChunkedReconciliation` — 25 candidates split into chunks
- Benchmark: `BenchmarkAddFacts_100ExistingFacts`, `BenchmarkAddFacts_1000ExistingFacts`

---

## Milestone 2: Transactional Entity Embeddings

**Problem**: `UpsertEntityEmbedding` (store.go:634-647) does DELETE then INSERT as two separate statements outside a transaction. Concurrent Add() calls can hit the gap between them.

**Fix**: Wrap in a transaction.

```go
func (s *Store) UpsertEntityEmbedding(entityID string, embedding []float32) error {
    tx, err := s.db.Begin()
    if err != nil { return err }
    defer tx.Rollback()

    // DELETE + INSERT inside tx
    tx.Exec("DELETE FROM entity_embeddings WHERE id = ?", entityID)
    tx.Exec("INSERT INTO entity_embeddings (id, embedding) VALUES (?, ?)", entityID, embJSON)
    return tx.Commit()
}
```

**Tests**:
- `TestConcurrentEntityEmbeddingUpsert` — parallel goroutines upserting same entity, no gaps

---

## Milestone 3: CleanupStaleRelations at Scale

**Problem**: `CleanupStaleRelations` (store.go:490-565) loads ALL entities and ALL facts into memory to do string containment checks — O(entities * deletedTexts * facts). OOM at scale.

**Fix**: Push the work into SQL. Instead of loading everything into Go, use SQL LIKE queries to check if an entity name still appears in any remaining fact.

```go
func (s *Store) CleanupStaleRelations(userID, namespace string, deletedTexts []string) (int, error) {
    // For each entity mentioned in deleted texts:
    //   SELECT COUNT(*) FROM facts WHERE user_id=? AND namespace=? AND LOWER(text) LIKE '%' || LOWER(entity.name) || '%'
    // If count == 0, the entity is orphaned -> delete its relations
}
```

Single query per affected entity instead of loading 100k facts into memory.

**Tests**:
- `TestCleanupStaleRelationsSQL` — verify same behavior as current impl
- Benchmark: `BenchmarkCleanupStaleRelations_1000Facts`

---

## Milestone 4: Default Embed Dimension 1536 to 256

**Problem**: Default 1536-dim embeddings waste storage and slow KNN scans for short fact/entity strings.

**Approach**: Matryoshka truncation, not hierarchy/funnel filtering. OpenAI's text-embedding-3-small is trained with Matryoshka Representation Learning — the first N dimensions of the full 1536-dim vector are already meaningful on their own. You request `dimensions: 256` in the API call, get a 256-dim vector back. Same API call, same price, 6x less storage, proportional KNN speedup.

NOT doing two-pass funnel (coarse 256-dim search → re-rank at 1536-dim). That's for million-scale vector DBs. faktory searches per-user (tens to hundreds of facts), so the candidate set is small. Single-pass at 256 is enough.

**Changes**:
1. `types.go:withDefaults()` — change `EmbedDimension` default from 1536 to 256
2. `embed.go` (or wherever `EmbedBatch`/`Embed` build the API request) — pass `dimensions` parameter in the embedding API request body so the API returns truncated vectors
3. README — update default table

If recall quality degrades, bump to 512 (still 3x savings). Only go back to 1536 if facts are long-form paragraphs (unlikely given extraction produces short atomic facts).

**Tests**:
- Integration test confirming 256-dim search still returns relevant results
- Benchmark: `BenchmarkSearchFacts_256dim` vs `BenchmarkSearchFacts_1536dim`

---

## Milestone 5: Real Test Coverage

**Problem**: Zero unit tests for extraction/reconciliation with mocked LLMs. Zero benchmarks. Zero concurrency tests. Zero tests for LLM garbage/partial JSON. The quality tests only run against one curated golden-path conversation.

### 5a. Unit tests with FakeCompleter
- `TestAddFacts_LLMReturnsEmptyFacts` — empty extraction
- `TestAddFacts_LLMReturnsGarbageJSON` — malformed response handling
- `TestAddFacts_LLMReturnsInvalidEventType` — unknown reconciliation event
- `TestAddFacts_LLMReturnsHallucinatedID` — ID not in existing set
- `TestAddFacts_PartialResponse` — some valid, some invalid actions
- `TestAddGraph_PronounsFiltered` — pronouns removed from entities
- `TestAddGraph_ValidatorTriggersCorrection` — errors trigger repass
- `TestAddGraph_CorrectionAlsoFails` — falls back to original

### 5b. Concurrency tests
- `TestConcurrentAdd_SameUser` — 10 goroutines calling Add() for same user
- `TestConcurrentAdd_DifferentUsers` — 10 goroutines, different users
- `TestConcurrentRecall_DuringAdd` — Recall while Add is in progress
- `TestConcurrentBumpAccess` — parallel BumpAccess calls

### 5c. Benchmarks
- `BenchmarkAdd_SmallStore` — 10 existing facts
- `BenchmarkAdd_MediumStore` — 100 existing facts
- `BenchmarkAdd_LargeStore` — 1000 existing facts (mocked LLM, real SQLite)
- `BenchmarkSearch_100Facts`, `BenchmarkSearch_1000Facts`, `BenchmarkSearch_10000Facts`
- `BenchmarkRecall_WithGraphExpansion`
- `BenchmarkCleanupStaleRelations`

### 5d. Adversarial quality tests
- Multi-language conversation (mixed English + French)
- Sarcasm / negation ("I definitely don't like pizza")
- Self-correction within same conversation ("Actually no, I meant Lyon not Paris")
- Long conversation (50+ turns)
- Minimal conversation ("hi" — should extract zero facts)

---

## Milestone 6: Recall Scaling

**Problem**: Hardcoded decay formula (alpha=0.01, beta=0.1) tuned for ~50 facts. At 5000 facts, temporal decay doesn't differentiate enough. KNN over-fetches globally then post-filters — O(total facts across all users).

### 6a. Configurable decay parameters
Add `DecayAlpha` and `DecayBeta` to Config with current values as defaults. Let power users tune.

### 6b. User-scoped KNN partition hint
The vec0 `k` parameter over-fetches by 20x then post-filters by user_id. At 100k total facts, this is wasteful. Explore:
- Separate vec0 table per user (too many tables?)
- Pre-filter approach: query fact IDs for user first, then KNN only against those
- Accept the limitation and document it: "best for <10k total facts across all users"

### 6c. Fact count in AddResult
Return `TotalFacts int` in AddResult so callers can monitor growth and decide when to prune.

---

## Milestone 7: Graph Pipeline Opt-In

**Problem**: Graph pipeline adds cost (1 LLM call + embeddings) and is already non-fatal. For use cases that only need flat fact storage, it's wasted spend.

**Fix**: Add `DisableGraph bool` to Config. When true, skip `addGraph()` entirely. Default false (current behavior).

```go
if !m.cfg.DisableGraph {
    wg.Add(1)
    go func() {
        defer wg.Done()
        graphTokens, graphEntities, graphRelations, graphErr = m.addGraph(...)
    }()
}
```

**Tests**:
- `TestDisableGraph_NoEntityExtraction` — verify no entity LLM call
- `TestDisableGraph_RecallStillWorks` — facts returned, no relations

---

## Ordering

Dependencies and priority:

```
M1 (reconciliation bomb)     — highest severity, blocks scale
  M1a (similarity gate)      — standalone, do first
  M1b (context cap)          — builds on M1a
  M1c (chunked reconcile)    — builds on M1b
M2 (tx entity embeddings)    — standalone, small, quick win
M3 (cleanup at scale)        — standalone, moderate
M4 (embed dim 256)           — standalone, one-line + tests
M5 (test coverage)           — independent, can start immediately
  M5a (unit tests)           — do first, enables confident refactoring
  M5b (concurrency tests)    — after M2
  M5c (benchmarks)           — after M1, validates improvements
  M5d (adversarial quality)  — independent
M6 (recall scaling)          — after M1 + M5c (needs benchmarks to validate)
M7 (graph opt-in)            — standalone, lowest priority
```

Parallelizable groups:
- **Group A**: M1a + M2 + M4 + M5a + M5d (all independent)
- **Group B**: M1b + M3 + M5b (after Group A)
- **Group C**: M1c + M5c + M7 (after Group B)
- **Group D**: M6 (after Group C, informed by benchmark data)

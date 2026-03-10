# Depth Stack: Retrieval Quality & Depth

## Goal

Make faktory's retrieval dramatically richer without changing the core API surface. A single `Recall()` call should return temporally-relevant, graph-connected, contextually-summarized memory — not just raw KNN hits.

## Principles

- Each layer is independent, composable, and testable in isolation
- No new top-level API methods required (layers enhance Recall internals)
- Measurement-first: each layer has a quality metric validated before and after

---

## Layer 1: Temporal — Freshness Scoring + Stale Relation Cleanup

### Problem

All facts are treated equally regardless of age or access frequency. A fact from 6 months ago ranks the same as one from yesterday. Relations never get cleaned up — "lives_in → Paris" persists alongside "lives_in → Marseille" after a move.

### Design

**Access tracking:**
- Add `access_count INTEGER DEFAULT 0` and `last_accessed_at TEXT` columns to `facts` table
- Bump both on every `SearchFacts` / `Recall` hit (batch UPDATE after query returns)
- No schema change to entities/relations (track at fact level only)

**Decay scoring:**
- After KNN returns raw cosine similarity, apply: `final_score = similarity * decay(age_days, access_count)`
- Decay function: `decay = 1 / (1 + α * age_days) * (1 + β * log(1 + access_count))`
  - `α = 0.01` (slow decay, ~50% at 100 days untouched)
  - `β = 0.1` (mild access boost)
  - Constants are hardcoded, not configurable (opinionated)
- Re-sort results by `final_score` after decay application

**Stale relation cleanup (rule-based, no LLM):**
- After fact reconciliation executes UPDATE or DELETE actions, collect affected entity names by scanning the old and new fact text for known entity names
- For UPDATE: if fact text changed and references a known entity, query relations involving that entity
- Apply simple rule: if a relation's source or target entity appears in a DELETED fact but not in any remaining fact for the user, mark the relation for deletion
- Log deletions for observability
- This is conservative — only removes relations where the backing fact is gone entirely

### Measurement

**Before (baseline):** Run the existing quality_test.go suite, record fact recall and relation recall scores.

**After:** Add a `TestTemporalScoring` that:
1. Inserts 10 facts at varying ages (mock `created_at`)
2. Searches — verifies recent facts rank higher than old ones with similar similarity
3. Bumps access on an old fact — verifies it climbs back up
4. Asserts stale relation cleanup: add "lives in Paris", update to "lives in Marseille", verify Paris relation removed

**Target:** No regression on existing quality metrics. Temporal test passes.

### Files Changed

- `store.go` — add columns, migration, bump access methods
- `memory.go` — decay scoring in Search/Recall, stale relation cleanup in addFacts after reconciliation
- `memory_test.go` — TestTemporalScoring
- `store_test.go` — test access tracking persistence

---

## Layer 2: Graph Traversal — Multi-Hop Context

### Problem

Recall only returns relations where source or target directly matches the query embedding. If you ask about Alice, you get "Alice works_at Acme" but not "Acme located_in San Francisco" — one hop away.

### Design

**New store method:**
```go
func (s *Store) ExpandRelations(entityIDs []string, userID string, maxDepth int, limit int) ([]Relation, error)
```
- BFS from seed entity IDs
- At each depth level, find all relations where source_id or target_id is in the current frontier
- Collect new entity IDs from results, add to frontier
- Stop at maxDepth (default 2) or when limit reached
- Deduplicate relations by ID

**Integration with Recall:**
- After initial KNN on entity_embeddings returns seed entity IDs, call `ExpandRelations` with depth=2
- Merge expanded relations into Recall result, preserving the direct-match ones first
- Summary formatter groups by hop distance: "Direct relationships:" then "Connected context:"

**Guard against noise:**
- Hard cap: maxDepth=2 (never more than 2 hops)
- Hard cap: limit=20 expanded relations total
- Only expand from entities with similarity > 0.5 (don't expand weak matches)

### Measurement

**Test:** `TestGraphTraversal`
1. Build a chain: Alice → works_at → Acme → located_in → SF → part_of → California
2. Recall("where does Alice work?") — should return Acme (direct) AND SF (1 hop) AND California (2 hops)
3. Recall with unrelated entity — should NOT pull in the Alice chain
4. Verify hop-distance ordering in summary

**Target:** Recall returns 2x more relevant context for entity-centric queries without returning noise for non-entity queries.

### Files Changed

- `store.go` — `ExpandRelations` method
- `memory.go` — integrate into Recall, update summary formatter
- `store_test.go` — TestExpandRelations (unit, no LLM)
- `memory_test.go` — TestGraphTraversal (integration)

---

## Layer 3: Summary — User Profile Generation

### Problem

Raw facts and relations are useful for machines but hard for an LLM to reason about efficiently. A consuming agent getting 15 bullet points and 8 relation triplets wastes context window on parsing structure instead of reasoning about content.

### Design

**New method:**
```go
func (m *Memory) Profile(ctx context.Context, userID string) (string, error)
```
- Fetches all facts (up to 200) and all relations (up to 100) for the user
- Sends to LLM with a PROFILE_GENERATION prompt:
  ```
  Summarize everything known about this user into a concise profile.
  Group by: personal details, work/education, preferences, relationships, plans.
  Skip empty groups. Use natural prose, not bullet points.
  Be concise — under 300 words.
  ```
- Returns the generated summary as a string

**Caching:**
- Store profile in a new `profiles` table: `(user_id TEXT PRIMARY KEY, summary TEXT, fact_hash TEXT, updated_at TEXT)`
- `fact_hash` = SHA256 of concatenated fact texts (sorted by ID for determinism)
- On `Profile()` call: compute current fact_hash, compare with stored. If match, return cached. If different, regenerate.
- `Add()` does NOT eagerly regenerate — profile is lazy, computed on read

**Integration with Recall:**
- New `RecallOptions` field: `IncludeProfile bool`
- When true, Recall prepends the cached profile to the summary
- Summary format becomes:
  ```
  User profile:
  [generated profile text]

  Relevant facts:
  - fact 1
  - fact 2

  Relationships:
  - Alice --works_at--> Acme
  ```

### Measurement

**Test:** `TestProfile`
1. Add a multi-fact conversation for a user
2. Call Profile() — verify it returns coherent prose covering all facts
3. Call Profile() again without changes — verify it returns cached (no LLM call, measure by token count = 0)
4. Add new facts — verify Profile() regenerates
5. Recall with IncludeProfile=true — verify summary contains profile section

**Target:** Profile covers 100% of stored facts in a readable format. Cache hit rate > 90% in normal usage (most Recalls don't follow an Add).

### Files Changed

- `store.go` — `profiles` table, GetProfile, UpsertProfile
- `memory.go` — Profile() method, integrate into Recall
- `prompts.go` — profileGenerationPrompt
- `types.go` — IncludeProfile in RecallOptions
- `memory_test.go` — TestProfile

---

## Path to C: Future Layers

These are NOT in scope for this implementation but are designed to slot in cleanly after the depth stack ships.

### C1: LLM Relation Reconciliation

Replace Layer 1's rule-based stale relation cleanup with an LLM call. After fact reconciliation, send the affected relations + updated facts to a RELATION_RECONCILE prompt that returns KEEP/DELETE/UPDATE actions. More accurate but adds cost per Add(). Only worth it if the rule-based approach shows measurable gaps.

### C2: Entity Merging

Detect duplicate entities via embedding similarity (threshold > 0.9). When candidates found, send to LLM for confirmation: "Are 'Bob' and 'Robert Smith' the same person given these facts?" If yes, merge: update all relations to point to the canonical entity, delete the duplicate. Risky — false merges are worse than duplicates. Needs a careful test suite before shipping.

### C3: Importance Scoring

LLM assigns importance (1-5) during fact extraction. Schema change: add `importance INTEGER DEFAULT 3` to facts. Recall weights: `final_score = similarity * decay * importance_weight`. Helps surface "I'm getting married next month" over "I had coffee today". Low risk, easy to add.

---

## Implementation Order

1. **Layer 1: Temporal** — foundation, touches store schema
2. **Layer 2: Graph Traversal** — builds on store, independent of Layer 1
3. **Layer 3: Summary** — builds on both (uses temporal-ranked facts + expanded relations)

Each layer: implement → test → measure → commit. No layer ships without its measurement test passing.

---

## Verification

After all three layers ship:

```bash
go test -count=1 ./... -short          # all tests pass
go build ./cmd/faktory/                 # CLI builds
go build ./cmd/faktory-mcp/            # MCP server builds
go test -run TestQuality -count=1 -v   # no quality regression
```

# R7: Qualifier Metadata Layer

## Context

Research survey of 10 arxiv papers on hyper-relational knowledge graphs for personal memory systems.
Key finding: **hyper-edges provide marginal retrieval improvement (+1.5% Mem0g over flat Mem0) but structured metadata enables powerful SQL-level filtering**. The value is not in embedding qualifiers into vectors but in storing them as explicit queryable columns.

Papers driving this design:
- PersonalAI (2506.17001): thesis edges + episodic edges, dual hyper-edge types for personal KGs
- Hyper-Relational KG Construction with LLMs (2403.11786): GPT-3.5 qualifier extraction = 0.01 precision exact-match but 0.77 recall BERTScore. Qualifiers are extractable but formatting is unreliable
- FormerGNN (2508.03280): StarE's qualifier compression into embeddings hurts performance. Keep qualifiers as explicit metadata, not vectors
- Wikidata reification schema (Hogan et al.): Statement + Qualifier two-table pattern is proven for relational DBs
- Structured Output Benchmark (2501.10868): nested JSON schemas drop coverage from 96% to 13-21%. Keep schema flat
- Mem0 (2504.19413): graph variant only +1.5% accuracy. Temporal/open-domain questions benefit most
- HOLMES (2406.06027): qualifier-based filtering saves 67% tokens in multi-hop QA

## Design Rationale

### Why NOT full hyper-edges
1. Extraction quality ceiling: gpt-4o-mini cannot reliably extract arbitrary key:value qualifiers (0.01 precision exact-match with GPT-3.5)
2. Embedding doesn't help: FormerGNN proves StarE-style qualifier compression into vectors degrades performance
3. Marginal retrieval gain: Mem0g's +1.5% doesn't justify the complexity
4. Schema complexity risk: nested qualifier arrays in JSON schema drop structured output compliance from 96% to ~20%

### Why qualifier metadata IS worth it
1. Temporal filtering: "what did I say last week?" becomes a SQL WHERE clause, not a semantic search
2. Provenance: knowing which conversation turn produced a fact enables better reconciliation
3. Confidence: LLM-assigned confidence allows threshold-based filtering in Recall
4. Token savings: HOLMES shows 67% fewer tokens when qualifiers enable pre-filtering before summary generation

### Design: flat fields, not a separate table

The Wikidata pattern (Statement + Qualifier tables) is designed for open-ended qualifier types. faktory has a fixed, small set of qualifier types. Flat columns on the `facts` table are simpler, faster (no JOINs), and sufficient.

We already have `valid_from` and `invalid_at` from R3. This feature adds `source` and `confidence` as two more qualifier columns, plus wires qualifier-aware filtering into Recall.

## Schema Changes

### facts table — add 2 columns

```sql
ALTER TABLE facts ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE facts ADD COLUMN confidence INTEGER NOT NULL DEFAULT 0;
-- confidence: 0 = not set (backward compat), 1-5 scale (1=uncertain, 5=certain)
-- source: free-text, e.g. "turn:3" or "user:explicit" or "inferred"
```

No new tables. No new indexes (these columns are for filtering, not lookup).

### Why not a `fact_qualifiers` table

- faktory has exactly 4 qualifier types. A separate table adds JOIN overhead for zero flexibility gain.
- If we ever need open-ended qualifiers (v2+), we can add the table then.
- The 4 fixed types are already partially implemented: `valid_from` and `invalid_at` exist from R3.

## Extraction Changes

### ExtractedFact — add 2 optional fields

```go
type ExtractedFact struct {
    Text       string `json:"text"`
    Importance int    `json:"importance"`
    Source     string `json:"source,omitempty"`     // new
    Confidence int    `json:"confidence,omitempty"` // new
}
```

### factExtractionSchema — add optional fields (flat, not nested)

```json
{
  "text": {"type": "string"},
  "importance": {"type": "integer", "minimum": 1, "maximum": 5},
  "source": {"type": "string"},
  "confidence": {"type": "integer", "minimum": 1, "maximum": 5}
}
```

`source` and `confidence` are NOT in `required` — they're optional. If the LLM omits them, they default to `""` and `0`. This preserves backward compatibility and avoids the nested-schema quality cliff.

### Prompt addition

Append to `factExtractionPrompt`:

```
- Optionally, for each fact:
  - "source": who stated it — "user" if directly stated, "inferred" if derived from context
  - "confidence": how certain is this fact, 1 (guess) to 5 (explicitly stated)
  If unsure, omit these fields.
```

Light touch. The LLM can ignore these fields entirely and extraction still works.

### Gating

New Config field: `EnableQualifiers bool`

When false (default): extraction prompt and schema are unchanged. `source` and `confidence` columns exist but stay at defaults. Zero behavior change.

When true: extended prompt and schema are used. Extracted qualifiers flow into InsertFact.

## Store Changes

### InsertFact signature

```go
// Before
func (s *Store) InsertFact(userID, namespace, text, hash string, embedding []float32, importance int) (string, error)

// After
func (s *Store) InsertFact(userID, namespace, text, hash string, embedding []float32, importance int, source string, confidence int) (string, error)
```

Callers pass `""` and `0` when qualifiers are disabled. Same INSERT, two more bind params.

### SearchFacts — optional confidence filter

```go
func (s *Store) SearchFacts(queryEmbedding []float32, userID, namespace string, limit int, minConfidence int) ([]Fact, error)
```

When `minConfidence > 0`, add `AND confidence >= ?` to the WHERE clause. When 0, no filter (backward compat).

### Fact struct — add 2 fields

```go
type Fact struct {
    // ... existing fields ...
    Source     string `json:"source,omitempty"`
    Confidence int    `json:"confidence,omitempty"`
}
```

All existing Scan() calls gain two more columns. All SELECT queries gain `, source, confidence`.

## Recall Changes

### RecallOptions — add qualifier filters

```go
type RecallOptions struct {
    // ... existing fields ...
    MinConfidence int    `json:"min_confidence,omitempty"` // filter facts below this confidence
    ValidAt       string `json:"valid_at,omitempty"`       // point-in-time filter (ISO 8601)
}
```

### Recall implementation

```go
// In Recall(), after SearchFacts:
if opts != nil && opts.MinConfidence > 0 {
    // pass to SearchFacts as filter
}
if opts != nil && opts.ValidAt != "" {
    // use store_temporal.GetFactsAt() instead of SearchFacts
}
```

### Summary formatting — include qualifiers

When qualifiers are present, enrich the summary:

```
Relevant facts:
- likes Go (confidence: 5, source: user)
- lives in Lyon (confidence: 4, source: user)
- might be moving to Paris (confidence: 2, source: inferred)
```

This gives the consuming LLM signal about fact reliability without requiring it to understand the qualifier system.

## FakeCompleter Changes

### FactResult — add optional fields

```go
type FactResult struct {
    Text       string `json:"text"`
    Importance int    `json:"importance"`
    Source     string `json:"source,omitempty"`
    Confidence int    `json:"confidence,omitempty"`
}
```

Existing tests that don't set Source/Confidence continue to work (zero values).

## Test Plan

### qualifier_test.go (unit, no LLM)

1. **TestQualifiers_DisabledByDefault** — EnableQualifiers=false, verify source/confidence stay at defaults after Add
2. **TestQualifiers_ExtractedWhenEnabled** — EnableQualifiers=true, FakeCompleter returns facts with source+confidence, verify stored correctly
3. **TestQualifiers_ConfidenceFilter** — Add facts with varying confidence, Recall with MinConfidence=3, verify low-confidence facts excluded
4. **TestQualifiers_ValidAtFilter** — Add facts at different times, Recall with ValidAt, verify point-in-time filtering works (builds on R3)
5. **TestQualifiers_SummaryIncludesQualifiers** — Recall with qualified facts, verify summary text contains confidence/source annotations
6. **TestQualifiers_BackwardCompatible** — Existing tests pass unchanged when EnableQualifiers=false
7. **TestQualifiers_PartialExtraction** — FakeCompleter returns some facts with qualifiers, some without. Verify graceful handling of missing fields.

## Files Changed

| File | Change | Risk |
|------|--------|------|
| `types.go` | Add `Source`, `Confidence` to Fact; `MinConfidence`, `ValidAt` to RecallOptions; `EnableQualifiers` to Config | Low — additive |
| `prompts.go` | Conditional prompt extension + schema extension when EnableQualifiers=true | Med — prompt change affects extraction quality |
| `store.go` | 2 ALTER TABLE migrations, updated InsertFact/UpdateFact signatures, updated SELECT/Scan for new columns | Med — touches many queries |
| `memory.go` | Wire qualifiers from extraction to store, add filtering in Recall, enrich summary | Med — core logic |
| `faktorytest/fake.go` | Add Source/Confidence to FactResult | Low — additive |
| `qualifier_test.go` | New test file | None |
| `reconcile.go` | Pass source/confidence through reconciliation | Low |

## What's NOT in scope (v2+)

- **Open-ended qualifier table** (`fact_qualifiers` with arbitrary key/value) — only needed if we exceed 6-8 qualifier types
- **Qualifier-aware embeddings** — research shows this hurts (FormerGNN). If ever needed, concatenate qualifiers into fact text before embedding
- **Episodic edges** (PersonalAI-style session grouping) — R6 session summaries already cover this use case
- **Relation qualifiers** — qualifiers on relations (e.g., "works_at with qualifier start_date:2024"). Adds complexity to the graph pipeline for unclear gain
- **LLM-based qualifier reconciliation** — e.g., "is this confidence still valid?" Overkill for 4 fixed types

## Implementation Order

1. Schema + Store (migrations, InsertFact signature, Scan updates)
2. Types (Fact, RecallOptions, Config, ExtractedFact)
3. Prompts (conditional extension behind EnableQualifiers)
4. Memory (wire extraction → store, Recall filtering, summary enrichment)
5. FakeCompleter (additive FactResult fields)
6. Tests (all 7 cases)

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| gpt-4o-mini ignores qualifier fields | Medium | Low | Fields are optional; defaults are safe |
| gpt-4o-mini hallucinates confidence values | Medium | Medium | Validate 1-5 range; clamp or discard out-of-range |
| Schema change breaks existing Scan() | High | High | Add columns to ALL SELECT queries; run full test suite |
| Prompt change regresses fact extraction quality | Low | High | Gate behind EnableQualifiers; default OFF |
| InsertFact signature change breaks 13+ test files | High | Low | Mechanical — add `"", 0` to all existing call sites |

## Success Criteria

- `go test ./... -count=1 -short` passes with 0 failures
- All 7 qualifier tests pass
- Existing test suite unchanged when EnableQualifiers=false
- With EnableQualifiers=true, FakeCompleter round-trips source+confidence correctly
- Recall with MinConfidence filters correctly
- Summary text includes qualifier annotations when present

## Quality Validation (integration, requires API key)

After unit tests pass, validate with real LLM:

```bash
go test -tags=integration -run=TestQualifier -v
```

- Extract 10 conversations with EnableQualifiers=true
- Measure: what % of facts get non-default source/confidence values?
- Target: >50% of facts should have confidence set (source is harder to extract reliably)
- If <30%, the prompt needs tuning before shipping

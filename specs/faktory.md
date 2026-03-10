# Feature: faktory
## Status: READY

A minimal, opinionated fact memory store for AI agents. Go. Single binary. Single SQLite file. No plugin system.

~2K LOC. LLM-based fact extraction + vector search + graph memory. No external databases.

---

## Problem

**Who:** Developers building AI agents/chatbots that need to remember things about users across sessions.

**Pain:** Existing solutions carry massive complexity: factory patterns for many vector stores and LLM providers, TypeScript SDKs, platform clients, telemetry, async modes. You need Neo4j for graph memory. You need to choose and configure a vector database. The core behavior (extract facts from conversations, reconcile with existing facts) is buried under layers of abstraction.

**Trigger:** Developer wants `agent.remember(conversation)` and `agent.recall(query)` without running Neo4j, Qdrant, or any external service.

**Impact:** Without this, developers either adopt complex solutions, build their own ad-hoc approach, or skip persistent memory entirely.

**Why Now:** The pattern of LLM-based fact extraction + vector search + graph memory is proven. The question is no longer "does this approach work?" but "can it be 100x simpler?"

---

## Algorithm

```
1. Parse messages
2. LLM call: extract facts from conversation → {"facts": [...]}
3. For each extracted fact:
   a. Embed the fact
   b. Vector search top-5 existing facts for same user
4. Collect all existing similar facts (deduplicated by ID)
5. Map UUIDs to integers (prevent LLM hallucinating IDs)
6. LLM call: given existing + new facts, decide ADD/UPDATE/DELETE/NOOP
7. Execute each action against vector store
8. In parallel: LLM call for graph entity extraction → upsert entities + relations to SQLite
```

### Key design decisions

1. **Integer ID mapping for LLM reconciliation** — UUIDs are mapped to `"0"`, `"1"`, `"2"` before sending to the reconciliation LLM, then mapped back. Prevents the LLM from hallucinating non-existent IDs.

2. **Hash-based dedup** — SHA256 hash of fact text to detect exact duplicates before involving the LLM.

3. **Parallel pipelines** — Fact extraction and graph extraction run concurrently with goroutines. Graph pipeline is non-fatal.

4. **User-scoped everything** — Every query is filtered by `user_id`. No cross-user leakage.

5. **Structured outputs** — `response_format: json_schema` with fallback to `json_object`. Retry once on parse failure. Fail explicitly.

---

## Certainty Map

### Known-Knowns

| Fact | Source | Confidence |
|---|---|---|
| LLM-based fact extraction works for personal preferences/details | Production usage across the industry | High |
| sqlite-vec supports KNN search with cosine distance | sqlite-vec docs, benchmarks up to 1M vectors | High |
| OpenAI-compatible API is a de facto standard (Ollama, vLLM, Together, Groq all support it) | Industry adoption | High |
| 3 LLM calls per `Add()` is the minimum for facts + reconciliation + graph | Algorithmic constraint | High |
| Go can load sqlite-vec as extension via `mattn/go-sqlite3` | sqlite-vec Go examples, tested | High |

### Known-Unknowns

| Question | Impact if Wrong | Resolution |
|---|---|---|
| How well does `gpt-4o-mini` handle the reconciliation prompt with >20 existing facts? | Poor reconciliation = duplicates or lost facts | Test with 50+ facts corpus, measure precision |
| Does Ollama's `json_object` mode produce reliable structured output for our schemas? | Ollama users get parse errors | Test with llama3, mistral, qwen. Fallback: raw JSON parse with retry |
| sqlite-vec performance at 10K+ vectors with cosine distance? | Slow `Search()` beyond target | Benchmark. If slow, add pre-filtering by user_id before KNN |

### Assumptions

| Assumption | If Wrong | Validation |
|---|---|---|
| `gpt-4o-mini` is cheap enough that 3 calls per `Add()` is acceptable | Users complain about cost | Track tokens in response. Expose in `AddResult`. |
| sqlite-vec cosine similarity is good enough without reranking | Search quality suffers | Benchmark against known corpus |
| Single-file SQLite is sufficient (no need for external DB) | Concurrent access patterns from multiple processes break | Document: single-process only. WAL mode helps but not guaranteed. |
| Users want a CLI more than an HTTP API | Nobody uses the CLI, everyone wants HTTP | CLI first. HTTP wrapper is trivial to add in v0.2. |

---

## Scope

### In (v1)

- `Add(messages, userID)` — extract facts + entities/relations via LLM, reconcile, store
- `Search(query, userID, limit)` — embed query, KNN search, return facts
- `Get(factID)` — retrieve single fact
- `GetAll(userID, limit)` — list all facts for user
- `Update(factID, text)` — manual fact update + re-embed
- `Delete(factID)` — delete fact + embedding
- `DeleteAll(userID)` — wipe all data for user
- `SearchRelations(query, userID, limit)` — find graph relations matching a query
- `GetAllRelations(userID, limit)` — list all relations for user
- CLI: `faktory add`, `faktory search`, `faktory facts`, `faktory relations`, `faktory delete`, `faktory delete-all`
- Config: TOML file + env vars + code defaults
- SQLite + sqlite-vec in a single `.db` file
- OpenAI-compatible LLM/embedding endpoint (configurable base URL)
- Structured JSON outputs with `response_format`

### Out (Not v1)

| Feature | Reason |
|---|---|
| HTTP server | Trivial wrapper, add in v0.2 if needed |
| Multiple vector store backends | The whole point is to not have plugins |
| Multiple LLM providers | One OpenAI-compatible client covers all |
| Async/streaming | Go concurrency handles parallelism natively |
| Telemetry/analytics | No tracking |
| Auth/multi-tenancy | Single-user or trusted multi-user via `user_id` |
| Webhooks | No event system |
| Custom prompts | Iterate on hardcoded prompts. Fork if you want different prompts. |
| Reranking | Vector search quality is sufficient for v1 |
| Fact versioning/history | UPDATE overwrites. Add a history table later if needed. |
| Memory categories/tags | Flat fact list. Search handles discovery. |
| Procedural memory | Niche. Out of scope. |

### Future (v2+)

- HTTP API with OpenAPI spec
- Fact history / audit log
- Batch `Add()` for bulk import
- Configurable prompts via config file
- MCP server mode (Model Context Protocol)

### Anti-Goals

- **No plugin system.** One vector store. One LLM client. One embedding client. Fork to change.
- **No ORM.** Raw SQL queries against SQLite. The schema is 4 tables.
- **No managed platform.** This is a local-first tool.

---

## Data Model

```sql
CREATE TABLE facts (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    text        TEXT NOT NULL,
    hash        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE VIRTUAL TABLE fact_embeddings USING vec0(
    id          TEXT PRIMARY KEY,
    embedding   float[?]            -- dimension from config
);

CREATE TABLE entities (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(user_id, name, type)
);

CREATE VIRTUAL TABLE entity_embeddings USING vec0(
    id          TEXT PRIMARY KEY,
    embedding   float[?]            -- dimension from config
);

CREATE TABLE relations (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    source_id   TEXT NOT NULL REFERENCES entities(id),
    relation    TEXT NOT NULL,
    target_id   TEXT NOT NULL REFERENCES entities(id),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(user_id, source_id, relation, target_id)
);

CREATE INDEX idx_facts_user ON facts(user_id);
CREATE INDEX idx_entities_user ON entities(user_id);
CREATE INDEX idx_relations_user ON relations(user_id);
CREATE INDEX idx_relations_source ON relations(source_id);
CREATE INDEX idx_relations_target ON relations(target_id);
```

All timestamps UTC, RFC 3339. IDs are UUIDs as strings. Embedding dimension set at DB creation time, immutable after.

---

## Write Path: `Add()`

```
messages ─┬─→ [Fact Pipeline]  ──→ facts + embeddings in SQLite
          └─→ [Graph Pipeline] ──→ entities + relations in SQLite
          (concurrent goroutines, both must complete, graph failure non-fatal)

Fact Pipeline:
  1. Truncate messages if they exceed ~100K chars (~25K tokens). Log warning.
  2. LLM call: FACT_EXTRACTION_PROMPT(messages) → {"facts": [...]}
  3. For each fact:
     a. SHA256 hash → if exact match in DB for this user → skip (NOOP, no LLM needed)
     b. Embed the fact text
     c. KNN search top-5 existing facts for same user_id
  4. Collect all similar existing facts, deduplicate by ID
  5. Map existing fact UUIDs to sequential integers ("0", "1", ...)
  6. LLM call: RECONCILE_PROMPT(existing_facts, new_facts) → [actions]
  7. For each action:
     ADD    → INSERT fact + INSERT embedding
     UPDATE → UPDATE fact text + hash + updated_at, DELETE old embedding, INSERT new embedding
     DELETE → DELETE fact + DELETE embedding
     NOOP   → nothing

Graph Pipeline:
  1. Truncate messages (same as fact pipeline).
  2. LLM call: ENTITY_EXTRACTION_PROMPT(messages) → {entities, relations}
  3. For each entity: INSERT OR IGNORE (deduplicate by user_id + name + type), embed entity name, upsert entity_embeddings
  4. For each relation: resolve source/target entity IDs, INSERT OR IGNORE

Token tracking:
  - All LLM calls accumulate total_tokens from the API response.
  - AddResult.Tokens reports combined tokens from fact + graph pipelines.
```

---

## Read Path

**`Search(query, userID, limit)`**
```
embed(query) → sqlite-vec KNN(embedding, limit, WHERE user_id = ?) → return facts
```
No LLM in the read path. Embedding + KNN only. Target: < 500ms.

**`SearchRelations(query, userID, limit)`**
```
embed(query) → sqlite-vec KNN(entity_embeddings, limit, WHERE user_id = ?)
→ collect matching entity IDs
→ SELECT relations WHERE (source_id IN entity_ids OR target_id IN entity_ids) AND user_id = ?
→ return matching triplets
```
No LLM call for relation search. Embedding similarity on entity names (entities are embedded at insertion time in the graph pipeline). Same approach as fact search.

---

## Prompts

Three prompts. Short. No verbose few-shot chains. Rely on structured outputs (`response_format: json_schema`) to enforce format.

### FACT_EXTRACTION

```
You extract discrete facts from conversations.

Rules:
- Extract facts from user messages only. Ignore assistant and system messages.
- Each fact is a short, self-contained statement.
- Use the same language as the input.
- No opinions about the conversation. No meta-commentary.
- If nothing worth remembering, return an empty list.
- Focus on: preferences, personal details, plans, professional info, relationships.
```

Schema: `{"facts": ["string"]}`

### RECONCILE_MEMORY

```
You manage a fact store. Given existing facts and newly extracted facts,
decide what to do with each.

Operations:
- ADD: new information not present in existing facts. Generate a new id.
- UPDATE: corrects, enriches, or supersedes an existing fact. Keep same id.
- DELETE: contradicts an existing fact. Keep same id.
- NOOP: already known. Keep same id.

Rules:
- Same meaning expressed differently → NOOP (e.g., "likes pizza" vs "enjoys pizza").
- New value replaces old → UPDATE (e.g., "lives in Paris" → "moved to Lyon" = UPDATE to "lives in Lyon").
- Direct contradiction → DELETE old + ADD new.
- Preserve the language of the facts.
- Only use IDs from the existing facts list. Generate new IDs only for ADD.
```

Schema: `{"memory": [{"id": "string", "text": "string", "event": "ADD|UPDATE|DELETE|NOOP", "old_memory": "string|null"}]}`

### ENTITY_EXTRACTION

```
Extract entities and their relationships from the conversation.

Entity types: person, organization, place, product, event, concept, other.
Relations: use short snake_case verbs (works_at, lives_in, likes, owns, married_to, ...).

Rules:
- Only extract what is explicitly stated or strongly implied.
- Do not invent relations.
- Normalize entity names: capitalize proper nouns.
- Use the same language as the input for entity names.
```

Schema: `{"entities": [{"name": "string", "type": "string"}], "relations": [{"source": "string", "relation": "string", "target": "string"}]}`

### Structured output strategy

```
Priority:
1. response_format: {type: "json_schema", json_schema: {schema}} — OpenAI, vLLM
2. response_format: {type: "json_object"} + schema in prompt — Ollama, Groq
Detection: try (1), if 400 error, fall back to (2) and cache the choice.
```

---

## Edge Cases

| Scenario | Expected Behavior | Severity |
|---|---|---|
| Empty message / "Hi" / small talk | 0 facts, 0 relations. Empty `AddResult`. No error. | P1 |
| Same messages added twice | All facts NOOP on second call. Idempotent. | P1 |
| Contradictory facts ("lives in Paris" then "moved to Lyon") | Old fact UPDATED to "lives in Lyon". Single fact remains. | P1 |
| LLM returns invalid JSON | Retry once. If still invalid, return error with raw response. | P1 |
| LLM returns facts from assistant messages | Prompt instructs to ignore. If it happens, facts are stored (accepted risk). | P2 |
| Very long conversation (> LLM context window) | Truncate to last N messages that fit. Log warning. | P2 |
| user_id empty string | Error immediately. Not optional. | P1 |
| Embedding dimension mismatch (config vs actual) | Error on first `Add()`. Clear message. | P1 |
| sqlite-vec extension not loadable | Error on `New()`. "sqlite-vec extension required, see install guide." | P1 |
| Search with 0 facts in DB | Return empty list, no error. | P1 |
| 1000+ facts for one user | Vector search still fast (sqlite-vec handles this). Reconciliation only sees top-5 similar, not all. | P2 |
| Concurrent `Add()` from multiple goroutines | SQLite WAL mode serializes writes. Safe but sequential. | P3 |
| Non-English input | Facts extracted in input language. Vector search works cross-language via embeddings. | P2 |
| Entity name variations ("Bob", "Robert", "Bob Smith") | Stored as separate entities. No merge heuristic. Accepted limitation. | P3 |

---

## Failure Modes

| What Fails | User Sees | Recovery |
|---|---|---|
| LLM API unreachable | `Add()` returns error with context (URL, status) | Retry. Check base URL and API key. |
| LLM returns empty facts | Empty `AddResult`. No error. | Normal for trivial input. |
| Embedding API unreachable | `Add()` / `Search()` returns error | Same endpoint as LLM typically. Check connectivity. |
| SQLite file locked by another process | `New()` or write operations return error | Single-process design. Document this. |
| Disk full | SQLite write fails, error propagated | Standard OS-level issue. |
| sqlite-vec KNN returns no results | `Search()` returns empty list | Normal if no facts stored for user. |
| Corrupted `.db` file | `New()` fails or queries return errors | Restore from backup. SQLite is a file; back it up. |

---

## Success Criteria

### Functional (Must)

- `Add()` with "Je m'appelle Alice, j'habite a Lyon, je travaille chez Acme" creates 3 facts and 3+ relations.
- Second `Add()` with same messages returns all NOOP. No duplicates.
- `Add()` with "J'ai demenage a Marseille" updates the location fact. `GetAll()` shows "Marseille", not "Lyon".
- `Search("ou habite Alice?")` returns the location fact as top result.
- `GetAllRelations()` returns (Alice, works_at, Acme), (Alice, lives_in, Marseille).
- `Delete(factID)` removes fact from both `facts` table and `fact_embeddings`.
- `DeleteAll(userID)` removes all facts, embeddings, entities, and relations for that user.
- Facts for user "alice" never appear in results for user "bob".

### Quality (Should)

| Metric | Target |
|---|---|
| `Add()` latency (short message, gpt-4o-mini) | < 3 seconds |
| `Search()` latency (1K facts) | < 500ms |
| LLM calls per `Add()` | exactly 3 (extract + reconcile + graph) |
| Binary size | < 50MB |
| Zero-config startup | `FAKTORY_API_KEY=sk-... faktory add "..." --user bob` works without config file |
| DB portability | Copy `faktory.db` to another machine, it works |
| Test coverage on write path | Reconciliation logic (ADD/UPDATE/DELETE/NOOP) covered with table-driven tests |

### User Outcome

A developer can `go install` faktory, export one env var, and have persistent fact memory for their AI agent in under 60 seconds. No Docker, no external databases, no YAML config files.

### Demo Script

```bash
export FAKTORY_API_KEY="sk-..."

# First interaction
faktory add "Je m'appelle Alice. J'habite a Lyon. \
  Je suis dev Go chez Acme Corp. J'adore le mass thaï." --user alice

faktory facts --user alice
# → Name is Alice
# → Lives in Lyon
# → Works as Go developer at Acme Corp
# → Likes Thai massage

faktory relations --user alice
# → Alice --works_at--> Acme Corp
# → Alice --lives_in--> Lyon
# → Alice --works_as--> Go developer

# Contradiction
faktory add "J'ai demenage a Marseille la semaine derniere" --user alice

faktory facts --user alice
# → Name is Alice
# → Lives in Marseille          ← updated, not Lyon
# → Works as Go developer at Acme Corp
# → Likes Thai massage

faktory search "where does alice live?" --user alice
# → Lives in Marseille

faktory relations --user alice
# → Alice --lives_in--> Marseille   ← updated
# → Alice --works_at--> Acme Corp

# Isolation
faktory search "alice" --user bob
# → (empty)
```

---

## Decisions

| Decision | Rationale | Reversible? |
|---|---|---|
| Go | Single binary, native concurrency, no runtime. | No |
| SQLite + sqlite-vec | Zero external dependencies. Single file. Sufficient for 100K+ facts. | Hard (schema migration needed) |
| OpenAI-compatible HTTP only | One client covers OpenAI, Ollama, vLLM, Together, Groq. No factory pattern. | Yes (add clients later) |
| No plugin system | The entire point. Opinionated = less code, less config, less bugs. Fork to customize. | Yes (but defeats the purpose) |
| Graph in SQLite (not Neo4j) | 3 tables of triplets don't need a graph database. SQL JOINs handle traversal. | Yes |
| Structured outputs over regex parsing | More reliable. Fail explicitly instead of parsing heroics. | Yes |
| user_id mandatory, no agent_id/run_id | Simpler scoping. One dimension of isolation. Add columns later if needed. | Yes |
| UTC timestamps only | No timezone bugs. | No |
| No telemetry | Respect for users. | Yes (add opt-in later) |
| CLI before HTTP | Validate the core before adding a server. HTTP is a trivial wrapper over the lib. | Yes |

---

## Known Limitations

1. **Relation reconciliation** — Relations use INSERT OR IGNORE. If a user moves from Lyon to Marseille, the old relation (lives_in → Lyon) persists alongside the new one. Fact reconciliation handles the update, but the graph does not merge or delete stale relations.

2. **Entity name variations** — "Bob", "Robert", "Bob Smith" are stored as separate entities. No merge heuristic.

3. **Relation name normalization** — The prompt asks for snake_case, but there's no post-processing validation. The LLM may produce inconsistent relation names across calls.

4. **Multilingual** — Prompts are English instructions. The LLM is told to "use the same language as the input" but extraction quality may degrade for non-English input.

---

## Changelog
- 2026-03-10: Spec updated. Embedding-based SearchRelations, TOML config, conversation truncation, token tracking.
- 2026-03-09: Created. Status: READY.

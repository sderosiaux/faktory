# faktory

Memory library for Go agents. Give your agent persistent, structured memory across sessions with 3 lines of code.

```go
mem, _ := faktory.New(faktory.Config{LLMAPIKey: "sk-..."})
mem.Add(ctx, messages, userID)                   // extract & reconcile facts + entity graph
recall, _ := mem.Recall(ctx, query, userID, nil) // facts + graph traversal + profile
mem.Search(ctx, query, userID, 5, faktory.WithNamespace("work")) // namespace isolation
```

Single SQLite file. No external services. Works with any OpenAI-compatible API.

## The agent integration pattern

faktory is designed to drop into your agent's conversation loop:

```go
func handleMessage(ctx context.Context, mem *faktory.Memory, userID string, messages []faktory.Message) string {
    // Load relevant context for this conversation
    lastMsg := messages[len(messages)-1].Content
    recall, _ := mem.Recall(ctx, lastMsg, userID, &faktory.RecallOptions{
        IncludeProfile: true,
    })

    // Inject into system prompt
    system := basePrompt + "\n\nUser context:\n" + recall.Summary

    // Get LLM response
    response := myLLM.Chat(system, messages)

    // Store new memories (fire and forget)
    go mem.Add(ctx, messages, userID)

    return response
}
```

That's it. faktory handles fact extraction, contradiction resolution, entity graphs, temporal relevance, and user profiling behind the scenes.

## Use cases

**Customer support agent** — User chats over weeks. faktory remembers their plan, past issues, preferences. `Recall("billing question")` pulls relevant history without the user repeating themselves. Use `WithNamespace(ticketID)` to isolate per-ticket context.

**Personal assistant** — User tells it about their life over time. faktory builds the entity graph (people, places, relationships). "Remind me about my trip plans" works because the graph connects Tokyo to Emma to Sophie.

**Sales agent** — Each prospect gets a user_id. The agent remembers what was discussed, objections raised, preferences. `Profile()` gives the team a snapshot before the next call.

**Tutoring agent** — Remembers what the student knows and struggles with. Temporal decay naturally deprioritizes topics the student has mastered.

**Multi-tenant SaaS** — Isolate memories per tenant with `WithNamespace(tenantID)`. Same user_id, completely separate memory spaces.

## Install

```bash
go get github.com/sderosiaux/faktory
```

Requires an OpenAI-compatible API (OpenAI, Ollama, vLLM, Together, Groq, etc.).

## API

### Write path

```go
// Create a memory instance (once, at startup)
mem, err := faktory.New(faktory.Config{
    LLMAPIKey: "sk-...",
    // LLMBaseURL, LLMModel, EmbedModel, EmbedDimension, DBPath — all have defaults
    // Logger: slog.Default(), // opt-in structured logging (silent by default)
})
defer mem.Close()

// Store facts from a conversation (3 LLM calls)
result, err := mem.Add(ctx, []faktory.Message{
    {Role: "user", Content: "I'm Alice, I live in Lyon, I work at Acme."},
}, "alice")
// result.Added, result.Updated, result.Deleted, result.Tokens

// Namespace-scoped — isolate memories by project, tenant, or conversation
result, err = mem.Add(ctx, messages, "alice", faktory.WithNamespace("project-x"))
```

`Add()` extracts facts, reconciles contradictions (UPDATE replaces "lives in Lyon" with "lives in Marseille"), builds an entity graph, and cleans up stale relations.

### Read path

```go
// One-shot retrieval: facts + multi-hop graph + optional profile
recall, err := mem.Recall(ctx, "where does alice work?", "alice", &faktory.RecallOptions{
    MaxFacts:       10,
    MaxRelations:   10,
    IncludeProfile: true,      // prepend cached user profile
    Namespace:      "project-x", // scope to namespace (empty = all)
})
// recall.Facts, recall.Relations, recall.Summary (pre-formatted for system prompt)

// Semantic search (with temporal decay re-ranking)
facts, err := mem.Search(ctx, "where does alice live?", "alice", 5)

// Entity graph search
rels, err := mem.SearchRelations(ctx, "alice workplace", "alice", 5)

// User profile (cached, regenerates when facts change)
profile, err := mem.Profile(ctx, "alice")

// Direct access
fact, err := mem.Get(ctx, factID)
facts, err := mem.GetAll(ctx, "alice", 100)
rels, err := mem.GetAllRelations(ctx, "alice", 100)
```

No LLM calls on the read path except profile generation on cache miss.

### Management

```go
mem.Update(ctx, factID, "new text")      // re-embeds automatically
mem.Delete(ctx, factID)
mem.DeleteAll(ctx, "alice")              // all facts, entities, relations, profile
mem.DeleteAll(ctx, "alice", faktory.WithNamespace("work")) // only the "work" namespace
mem.Export(ctx, "alice", writer)          // JSONL backup
mem.Import(ctx, "alice", reader)         // restore from JSONL
```

## How it works

```
messages ─┬─→ [Fact Pipeline]  ──→ facts + embeddings in SQLite
          └─→ [Graph Pipeline] ──→ entities + relations in SQLite
          (concurrent, graph failure is non-fatal)
```

**Fact Pipeline:**
1. LLM extracts atomic facts from user messages
2. Hash dedup + embedding, vector search for similar existing facts
3. LLM reconciles: ADD / UPDATE / DELETE / NOOP for each fact
4. Stale relations cleaned up when a fact change removes the last mention of an entity

**Graph Pipeline:**
1. LLM extracts entities (person, org, place...) and relations (works_at, lives_in...)
2. Validated (no pronouns, no self-referential relations, no duplicates), re-prompted once on errors
3. Entities embedded for semantic search, stored as triplets

**Recall:**
1. Embed query once, run parallel KNN on facts and entity embeddings
2. Re-rank facts with temporal decay: `score = similarity * (1/(1+0.01*age_days)) * (1+0.1*ln(1+access_count))`
3. Expand relations via BFS (up to 2 hops from matched entities with similarity > 0.5)
4. Optionally prepend a cached user profile (LLM-generated summary of all facts)
5. Return structured data + pre-formatted summary ready for system prompt injection

## Configuration

```go
faktory.Config{
    DBPath:         "faktory.db",                   // SQLite file path
    LLMBaseURL:     "https://api.openai.com/v1",    // any OpenAI-compatible API
    LLMAPIKey:      "sk-...",                        // required
    LLMModel:       "gpt-4o-mini",                  // chat model
    EmbedModel:     "text-embedding-3-small",        // embedding model
    EmbedDimension: 256,                             // vector dimension (Matryoshka truncation)
    Logger:         slog.Default(),                  // nil = silent (default)

    DisableGraph: false, // skip entity/relation extraction (saves 1 LLM call per Add)

    // Custom prompts — override LLM system prompts for domain-specific tuning
    PromptFactExtraction:   "",  // fact extraction (empty = default)
    PromptReconciliation:   "",  // fact reconciliation (empty = default)
    PromptEntityExtraction: "",  // entity + relation extraction (empty = default)
}
```

Or via env vars for the CLI/MCP server:

| Env Var | Default |
|---|---|
| `FAKTORY_DB` | `faktory.db` |
| `FAKTORY_BASE_URL` | `https://api.openai.com/v1` |
| `FAKTORY_API_KEY` | (required) |
| `FAKTORY_MODEL` | `gpt-4o-mini` |
| `FAKTORY_EMBED_MODEL` | `text-embedding-3-small` |
| `FAKTORY_EMBED_DIM` | `256` |

Works with Ollama, vLLM, Together, Groq — anything that speaks the OpenAI chat/embedding API.

## CLI

A debugging/exploration tool. Not the primary interface.

```bash
go install github.com/sderosiaux/faktory/cmd/faktory@latest

faktory add "I live in Lyon" --user alice
faktory recall "where does alice live?" --user alice --profile
faktory facts --user alice
faktory relations --user alice
faktory profile --user alice
faktory search "lyon" --user alice
faktory get [fact-id]
faktory update [fact-id] [new text]
faktory delete [fact-id]
faktory delete-all --user alice
faktory export --user alice > backup.jsonl
faktory import backup.jsonl --user alice
```

## MCP Server

For integration with AI tools that support MCP (Claude Code, Cursor, etc.):

```bash
go install github.com/sderosiaux/faktory/cmd/faktory-mcp@latest
```

```json
{
  "mcpServers": {
    "faktory": {
      "command": "faktory-mcp",
      "env": { "FAKTORY_API_KEY": "sk-..." }
    }
  }
}
```

Tools: `memory_add`, `memory_recall`, `memory_search`, `memory_profile`, `memory_get_all`, `memory_search_relations`, `memory_delete`, `memory_delete_all`.

## Design decisions

- **Go library first** — CLI and MCP are secondary. The primary interface is `faktory.New()` + `Add()` + `Recall()`
- **SQLite + sqlite-vec** — Zero dependencies. Single file. Portable. No server to run
- **OpenAI-compatible only** — One HTTP client covers every provider
- **3 LLM calls per Add()** — Extract + reconcile + graph. This is the minimum for reliable memory
- **Integer ID mapping** — UUIDs mapped to sequential ints before sending to the reconciliation LLM. Prevents hallucinated IDs
- **Opinionated decay** — α=0.01, β=0.1. Hardcoded. Recent and frequently accessed facts rank higher
- **Conservative cleanup** — Relations are only pruned when their entity disappears from all facts
- **Lazy profiles** — Generated on read, cached until facts change. No LLM calls on Add()
- **Silent by default** — No logging unless you pass a `*slog.Logger`. Libraries shouldn't pollute stderr
- **Custom prompts over plugins** — Override extraction/reconciliation prompts via Config for domain tuning. No plugin system needed
- **256-dim default** — text-embedding-3-small supports Matryoshka truncation. 256 dimensions retain quality for short fact strings with 6x less storage. Override with `EmbedDimension: 1536` if needed
- **Namespace scoping** — Per-call `WithNamespace()` adds a second isolation dimension beyond user_id

## Testing

```bash
go test ./...                           # unit tests only (free, no API calls)
go test -tags=integration ./...         # unit + integration + quality (requires OPENAI_API_KEY)
```

## License

MIT

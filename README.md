# faktory

Persistent fact memory for AI agents. Single binary. Single SQLite file. ~2K LOC.

faktory extracts facts from conversations using an LLM, reconciles them with existing knowledge, and builds a queryable entity graph — all stored in a single `.db` file with no external dependencies.

## Why

You're building an AI agent that talks to users. You want it to remember things: preferences, personal details, relationships, plans. Across sessions. Without running Neo4j, Qdrant, Postgres, or any external service.

```
User: "I'm Alice, I live in Lyon, I work at Acme as a Go developer."
Agent: (stores 4 facts + 3 entity relations)

User: "I just moved to Marseille."
Agent: (updates location fact, cleans up stale Lyon relation)

Later...
Agent: recall("where does Alice live?") → facts + graph traversal + optional profile
```

faktory does this with 3 LLM calls per `Add()`:
1. **Extract** facts from the conversation
2. **Reconcile** new facts against existing ones (ADD / UPDATE / DELETE / NOOP)
3. **Extract** entities and relationships into a graph

Everything stored in SQLite + [sqlite-vec](https://github.com/asg017/sqlite-vec) for vector search.

## Install

```bash
go install github.com/sderosiaux/faktory/cmd/faktory@latest
```

Requires an OpenAI-compatible API (OpenAI, Ollama, vLLM, Together, Groq, etc.).

## Quick Start

```bash
export FAKTORY_API_KEY="sk-..."

# Remember something
faktory add "Je m'appelle Alice. J'habite a Lyon. Je suis dev Go chez Acme." --user alice

# See what was remembered
faktory facts --user alice
#   Name is Alice
#   Lives in Lyon
#   Works as Go developer at Acme

# See the entity graph
faktory relations --user alice
#   Alice --works_at--> Acme
#   Alice --lives_in--> Lyon

# Search by meaning
faktory search "where does alice live?" --user alice
#   [0.92] Lives in Lyon

# One-shot retrieval with graph traversal
faktory recall "tell me about alice" --user alice
#   Relevant facts:
#   - Name is Alice
#   - Lives in Lyon
#   Relationships:
#   - Alice --works_at--> Acme
#   - Acme --located_in--> Lyon  (multi-hop)

# With user profile summary
faktory recall "tell me about alice" --user alice --profile
#   User profile:
#   Alice is a Go developer working at Acme, living in Lyon...
#   Relevant facts: ...

# Update with a contradiction
faktory add "J'ai demenage a Marseille" --user alice
faktory facts --user alice
#   Lives in Marseille    ← updated, not Lyon

# Generate a standalone profile
faktory profile --user alice
#   Alice is a Go developer based in Marseille, working at Acme...

# Export/import for backup or migration
faktory export --user alice > alice.jsonl
faktory import alice.jsonl --user bob

# Users are isolated
faktory search "alice" --user bob
#   (no results)
```

## Use as a Library

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/sderosiaux/faktory"
)

func main() {
    mem, err := faktory.New(faktory.Config{
        LLMAPIKey: "sk-...",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer mem.Close()

    ctx := context.Background()

    // Store facts from a conversation
    result, err := mem.Add(ctx, []faktory.Message{
        {Role: "user", Content: "I'm Alice, I live in Lyon, I work at Acme."},
    }, "alice")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Added %d facts, used %d tokens\n", len(result.Added), result.Tokens)

    // Semantic search (re-ranked by temporal decay)
    facts, _ := mem.Search(ctx, "where does alice live?", "alice", 5)
    for _, f := range facts {
        fmt.Printf("[%.2f] %s\n", f.Score, f.Text)
    }

    // One-shot recall: facts + multi-hop graph + optional profile
    recall, _ := mem.Recall(ctx, "alice", "alice", &faktory.RecallOptions{
        MaxFacts:       10,
        MaxRelations:   10,
        IncludeProfile: true,
    })
    fmt.Println(recall.Summary)

    // Entity graph search
    rels, _ := mem.SearchRelations(ctx, "alice workplace", "alice", 5)
    for _, r := range rels {
        fmt.Printf("%s --%s--> %s\n", r.Source, r.Relation, r.Target)
    }

    // User profile (cached, regenerates when facts change)
    profile, _ := mem.Profile(ctx, "alice")
    fmt.Println(profile)
}
```

## How It Works

```
messages ─┬─→ [Fact Pipeline]  ──→ facts + embeddings in SQLite
          └─→ [Graph Pipeline] ──→ entities + relations in SQLite
          (concurrent, graph failure is non-fatal)
```

**Fact Pipeline:**
1. LLM extracts atomic facts from user messages
2. Each fact is hash-checked (exact dedup) then embedded
3. Vector search finds similar existing facts (top 5)
4. LLM reconciles: decides ADD / UPDATE / DELETE / NOOP for each
5. Actions executed against SQLite
6. Stale relations cleaned up: if a fact UPDATE/DELETE removes the last mention of an entity, its relations are pruned

**Graph Pipeline:**
1. LLM extracts entities (person, org, place...) and relations (works_at, lives_in...)
2. Extraction is validated (no pronouns, no self-referential relations, no duplicates)
3. Entities are embedded for semantic search
4. Stored as triplets in SQLite (source → relation → target)

**Search:**
- `Search()` embeds your query, runs KNN against fact embeddings, re-ranks with temporal decay
- `SearchRelations()` embeds your query, finds similar entities, returns their relations
- `Recall()` does both in parallel, expands relations via multi-hop BFS, optionally prepends a user profile
- No LLM calls in the read path (except profile generation on cache miss)

**Temporal Decay:**
- Facts are re-ranked after KNN using: `score = similarity * (1/(1+0.01*age_days)) * (1+0.1*ln(1+access_count))`
- Recent and frequently accessed facts rank higher
- Access counts are bumped asynchronously on every Search/Recall hit

**Graph Traversal:**
- `Recall()` finds seed entities via KNN (similarity > 0.5), then runs BFS up to 2 hops
- Returns direct relations and connected context (e.g., Alice → works_at → Acme → located_in → SF)

**User Profiles:**
- `Profile()` generates a concise LLM summary from all facts + relations
- Cached in SQLite, invalidated by a deterministic hash of fact content
- Can be prepended to Recall output via `IncludeProfile: true`

## MCP Server

faktory ships an MCP server for integration with AI tools (Claude Code, Cursor, etc.):

```bash
go install github.com/sderosiaux/faktory/cmd/faktory-mcp@latest
```

Configure in your MCP client:

```json
{
  "mcpServers": {
    "faktory": {
      "command": "faktory-mcp",
      "env": {
        "FAKTORY_API_KEY": "sk-..."
      }
    }
  }
}
```

**Available tools:**

| Tool | Description |
|---|---|
| `memory_add` | Extract and store facts from a conversation |
| `memory_search` | Search facts by semantic similarity |
| `memory_recall` | Facts + graph traversal + optional profile in one call |
| `memory_profile` | Generate a user profile summary |
| `memory_get_all` | List all stored facts |
| `memory_search_relations` | Search entity relations |
| `memory_delete` | Delete a specific fact |
| `memory_delete_all` | Delete all data for a user |

## Configuration

faktory loads config from (in priority order): **env vars > TOML file > code defaults**.

TOML file is searched at `./faktory.toml` then `~/.config/faktory/faktory.toml`.

```toml
db_path = "faktory.db"
llm_base_url = "https://api.openai.com/v1"
llm_api_key = "sk-..."
llm_model = "gpt-4o-mini"
embed_model = "text-embedding-3-small"
embed_dimension = 1536
```

| Env Var | TOML Key | Default |
|---|---|---|
| `FAKTORY_DB` | `db_path` | `faktory.db` |
| `FAKTORY_BASE_URL` | `llm_base_url` | `https://api.openai.com/v1` |
| `FAKTORY_API_KEY` | `llm_api_key` | (required) |
| `FAKTORY_MODEL` | `llm_model` | `gpt-4o-mini` |
| `FAKTORY_EMBED_MODEL` | `embed_model` | `text-embedding-3-small` |
| `FAKTORY_EMBED_DIM` | `embed_dimension` | `1536` |

### Using with Ollama

```bash
export FAKTORY_BASE_URL="http://localhost:11434/v1"
export FAKTORY_MODEL="llama3"
export FAKTORY_EMBED_MODEL="nomic-embed-text"
export FAKTORY_EMBED_DIM="768"

faktory add "I prefer dark mode and use vim" --user dev
```

## CLI Reference

```
faktory add [message...] --user ID       Extract and store facts
faktory search [query] --user ID         Search facts by meaning (with temporal decay)
faktory recall [query] --user ID         Facts + relations + optional profile in one call
  --max-facts N                            Max facts (default 10)
  --max-relations N                        Max relations (default 10)
  --profile                                Include generated user profile
faktory profile --user ID                Generate user profile summary
faktory facts --user ID                  List all facts
faktory relations --user ID              List entity relations
faktory get [fact-id]                    Get a single fact by ID
faktory update [fact-id] [new-text...]   Update a fact's text
faktory delete [fact-id]                 Delete a specific fact
faktory delete-all --user ID             Delete all data for a user
faktory export --user ID                 Export all data as JSONL
faktory import [file] --user ID          Import JSONL data
```

## Design Decisions

- **Go** — Single binary, native concurrency, no runtime
- **SQLite + sqlite-vec** — Zero dependencies. Single file. Portable
- **OpenAI-compatible only** — One HTTP client covers OpenAI, Ollama, vLLM, Together, Groq
- **No plugin system** — Opinionated. Fork to customize
- **No telemetry** — Respect for users
- **3 LLM calls per Add()** — Extract facts + reconcile + extract graph. This is the minimum
- **Integer ID mapping** — UUIDs mapped to "0", "1", "2" before sending to reconciliation LLM. Prevents hallucinated IDs
- **Temporal decay hardcoded** — α=0.01 (age), β=0.1 (access boost). Not configurable. Tuned for reasonable defaults
- **Conservative stale cleanup** — Only removes relations when their backing entity disappears from all facts. No false deletions
- **Lazy profile caching** — Profiles regenerate on read when facts change, not on every write. Avoids unnecessary LLM calls

## License

MIT

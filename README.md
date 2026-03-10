# faktory

Persistent fact memory for AI agents. Single binary. Single SQLite file. ~2K LOC.

faktory extracts facts from conversations using an LLM, reconciles them with existing knowledge, and builds a queryable entity graph — all stored in a single `.db` file with no external dependencies.

## Why

You're building an AI agent that talks to users. You want it to remember things: preferences, personal details, relationships, plans. Across sessions. Without running Neo4j, Qdrant, Postgres, or any external service.

```
User: "I'm Alice, I live in Lyon, I work at Acme as a Go developer."
Agent: (stores 4 facts + 3 entity relations)

User: "I just moved to Marseille."
Agent: (updates location fact, adds new relation)

Later...
Agent: recall("where does Alice live?") → "Lives in Marseille"
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

# Update with a contradiction
faktory add "J'ai demenage a Marseille" --user alice
faktory facts --user alice
#   Lives in Marseille    ← updated, not Lyon

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

    // Semantic search
    facts, _ := mem.Search(ctx, "where does alice live?", "alice", 5)
    for _, f := range facts {
        fmt.Printf("[%.2f] %s\n", f.Score, f.Text)
    }

    // Entity graph search
    rels, _ := mem.SearchRelations(ctx, "alice workplace", "alice", 5)
    for _, r := range rels {
        fmt.Printf("%s --%s--> %s\n", r.Source, r.Relation, r.Target)
    }
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

**Graph Pipeline:**
1. LLM extracts entities (person, org, place...) and relations (works_at, lives_in...)
2. Entities are embedded for semantic search
3. Stored as triplets in SQLite (source → relation → target)

**Search:**
- `Search()` embeds your query, runs KNN against fact embeddings
- `SearchRelations()` embeds your query, finds similar entities, returns their relations
- No LLM calls in the read path

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
faktory add [message...] --user ID    Extract and store facts
faktory search [query] --user ID      Search facts by meaning
faktory facts --user ID               List all facts
faktory relations --user ID           List entity relations
faktory delete [fact-id]              Delete a specific fact
faktory delete-all --user ID          Delete all data for a user
```

## Design Decisions

- **Go** — Single binary, native concurrency, no runtime
- **SQLite + sqlite-vec** — Zero dependencies. Single file. Portable
- **OpenAI-compatible only** — One HTTP client covers OpenAI, Ollama, vLLM, Together, Groq
- **No plugin system** — Opinionated. Fork to customize
- **No telemetry** — Respect for users
- **3 LLM calls per Add()** — Extract facts + reconcile + extract graph. This is the minimum
- **Integer ID mapping** — UUIDs mapped to "0", "1", "2" before sending to reconciliation LLM. Prevents hallucinated IDs

## License

MIT

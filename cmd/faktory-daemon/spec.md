# Feature: faktory-daemon
## Status: SPEC

A memory runtime that wraps the faktory library. Runs as a long-lived process. Exposes faktory over HTTP. Manages memory lifecycle automatically. Enables multi-agent access to a shared memory store.

~1K LOC target. Uses faktory as a library. No new storage layer.

---

## Problem

**Who:** Teams running multiple agents that need shared, persistent memory with automatic maintenance.

**Pain:** faktory is a Go library — single process, single writer, caller manages lifecycle. When you have 3 agents (support, sales, ops) that need to share user memory, you need:
- A server process so multiple agents can connect
- Lifecycle automation so nobody has to remember to call `Prune()` or `Summarize()`
- Visibility into memory health so you know when things degrade

Today this requires building a wrapper service, a cron job, and custom monitoring. Every team builds the same thing.

**Trigger:** Second agent added to a system that already uses faktory.

**Impact:** Without this, teams either hack a shared SQLite file (breaks under concurrent writes), run separate faktory instances per agent (memory is fragmented), or build a custom HTTP wrapper (reinventing the same code).

---

## What It Is

A single binary (`faktory-daemon`) that:
1. Wraps a `faktory.Memory` instance
2. Exposes it over HTTP (JSON API)
3. Runs lifecycle policies in the background (prune, consolidate)
4. Tracks basic health metrics

```
Agent A ──HTTP──┐
Agent B ──HTTP──┤──→ faktory-daemon ──→ faktory.Memory ──→ SQLite
Agent C ──HTTP──┘         │
                    background:
                    - prune stale facts
                    - auto-consolidate
                    - health metrics
```

---

## What It Is NOT

- Not a distributed system. Single process, single SQLite file.
- Not a platform. No auth, no user management, no billing.
- Not a replacement for faktory. The library stays the primary interface for single-agent Go programs.
- Not a gateway. No request routing, no load balancing, no service mesh.

---

## API

### Write

```
POST /v1/memory/add
{
  "user_id": "alice",
  "namespace": "support",        // optional
  "messages": [
    {"role": "user", "content": "I moved to Lyon"}
  ]
}
→ 200 {"added": [...], "updated": [...], "deleted": [...], "total_facts": 42, "tokens": 340}
```

```
POST /v1/memory/summarize
{
  "user_id": "alice",
  "namespace": "support",
  "messages": [...]
}
→ 200 {"ok": true}
```

### Read

```
POST /v1/memory/recall
{
  "user_id": "alice",
  "query": "where does alice live?",
  "namespace": "support",
  "max_facts": 10,
  "max_relations": 10,
  "include_profile": true,
  "min_confidence": 3
}
→ 200 {"facts": [...], "relations": [...], "summary": "..."}
```

```
POST /v1/memory/search
{
  "user_id": "alice",
  "query": "lyon",
  "namespace": "support",
  "limit": 5
}
→ 200 {"facts": [...]}
```

```
GET /v1/memory/facts?user_id=alice&namespace=support&limit=100
→ 200 {"facts": [...]}

GET /v1/memory/relations?user_id=alice&namespace=support&limit=100
→ 200 {"relations": [...]}

GET /v1/memory/profile?user_id=alice&namespace=support
→ 200 {"profile": "..."}
```

### Management

```
POST /v1/memory/prune
{
  "user_id": "alice",
  "namespace": "support",
  "max_age_hours": 2160,    // 90 days
  "min_importance": 2,
  "max_access_count": 1,
  "dry_run": true
}
→ 200 {"pruned": [...], "count": 7}
```

```
DELETE /v1/memory/user?user_id=alice&namespace=support
→ 200 {"ok": true}
```

### Health

```
GET /v1/health
→ 200 {"status": "ok", "uptime_seconds": 3600, "facts_total": 1234}

GET /v1/stats
→ 200 {
  "facts_total": 1234,
  "users_total": 56,
  "adds_total": 789,
  "recalls_total": 456,
  "prunes_total": 12,
  "last_prune_at": "2026-03-16T10:00:00Z",
  "last_consolidate_at": "2026-03-16T09:30:00Z"
}
```

All POST bodies are JSON. All responses are JSON. No streaming. No websockets.

---

## Lifecycle Policies

Configured via TOML config file or environment variables. Run in background goroutines.

```toml
[daemon]
listen = "127.0.0.1:8420"     # localhost only by default
api_key = ""                   # set to require Bearer auth on all requests

[faktory]
db_path = "faktory.db"
llm_api_key = "sk-..."
llm_base_url = "https://api.openai.com/v1"
llm_model = "gpt-4o-mini"
embed_model = "text-embedding-3-small"
embed_dimension = 256
disable_graph = false
enable_qualifiers = false

# Custom prompts — override for domain-specific tuning
# prompt_fact_extraction = ""
# prompt_reconciliation = ""
# prompt_entity_extraction = ""

[policies.prune]
enabled = true
interval = "24h"
max_age = "90d"
min_importance = 2
max_access_count = 0

[policies.consolidate]
enabled = true
threshold = 200          # per user — summarize when fact count exceeds this

[policies.compact_summaries]
enabled = false
max_summaries = 10       # merge oldest when count exceeds this (future)
```

All `[faktory]` fields can be overridden via env vars: `FAKTORY_API_KEY`, `FAKTORY_BASE_URL`, `FAKTORY_MODEL`, etc. `[daemon]` fields via `FAKTORY_DAEMON_LISTEN`, `FAKTORY_DAEMON_API_KEY`.

### Policy: prune

Runs on interval. For each user+namespace, calls `faktory.Prune()` with the configured criteria. Logs what was pruned.

### Policy: consolidate

After each `Add()`, checks fact count. If above threshold, calls `Summarize()`. Same as `ConsolidateThreshold` in faktory but server-side.

### Policy: compact_summaries (future)

When a user has >N summaries, merge the oldest ones into a single summary. Requires an LLM call. Not in v1.

---

## Security

### Threat model

The daemon is a network service wrapping a memory store. Three threat categories matter:

**1. Memory poisoning (paper's #1 open problem)**

A client (malicious or buggy agent) injects false facts that persist and influence future agent decisions. Example: agent A writes "user cancelled their subscription" when they didn't. Agent B reads this and acts on it.

Mitigations in v1:
- **Namespace isolation** — each agent writes to its own namespace. Agent A cannot write to agent B's namespace. Cross-namespace reads are allowed (read is safe), cross-namespace writes are blocked by config.
- **Write audit log** — every Add() is already tracked in `fact_history` with timestamps. Who wrote what is reconstructible.
- **Confidence filtering** — consumers use `MinConfidence` in Recall to skip low-confidence facts. Poisoned facts from inferred sources get filtered.

Not in v1 (future):
- Per-namespace API keys (agent identity)
- Fact provenance tracking (which API key wrote this fact)
- Anomaly detection (sudden spike in contradictions for a user)

**2. Unauthorized access**

Without auth, anyone on the network can read/write all memory.

Mitigations in v1:
- **API key auth** — single shared key, configured in TOML or env var. All requests must send `Authorization: Bearer <key>`. Simple but sufficient for internal networks.
- **Listen address** — default `127.0.0.1:8420` (localhost only). Explicit config required to bind to `0.0.0.0`.

```toml
[daemon]
listen = "127.0.0.1:8420"   # localhost only by default
api_key = "sk-daemon-..."    # required if set, all requests must include it
```

Not in v1:
- Per-agent API keys with namespace-scoped permissions
- mTLS
- OAuth / JWT

**3. Input validation**

The HTTP layer is the system boundary — all input must be validated before reaching faktory.

Rules:
- `user_id` — required, max 256 chars, alphanumeric + `-_:.` only
- `namespace` — optional, same charset as user_id, max 128 chars
- `messages` — required for Add, array of `{role, content}`, max 100 messages, max 200K chars total
- `query` — required for Recall/Search, max 10K chars
- `limit` — optional int, clamped to 1-1000
- Request body — max 1MB. Reject larger.

faktory uses parameterized SQL queries (no injection risk), but the daemon should reject garbage before it reaches the library.

**4. Secrets**

- LLM API key in config or env var. Never logged, never in responses, never in error messages.
- Config file should be 0600 permissions. Daemon logs a warning if it's world-readable.

### What we accept

- No encryption at rest (SQLite file is plaintext). Use OS-level disk encryption if needed.
- No TLS in the daemon itself. Put it behind a reverse proxy for TLS.
- No per-user encryption. All facts for all users are in the same DB with the same access.

---

## Extensibility

### What's customizable

- **Lifecycle policies** — configured via TOML. Add new policies by adding a `[policies.X]` section with `enabled`, `interval`, and policy-specific fields. Policy logic lives in `policies.go` — one function per policy.
- **Custom prompts** — faktory's `PromptFactExtraction`, `PromptReconciliation`, `PromptEntityExtraction` are exposed as TOML config fields. The daemon passes them through to `faktory.Config`.
- **LLM provider** — any OpenAI-compatible endpoint via `llm_base_url` config field.

### What's NOT customizable

- **HTTP handlers** — no middleware hooks, no plugin system. Fork to change.
- **Storage** — SQLite only. faktory's design constraint.
- **Custom endpoints** — no dynamic route registration. Add them in `server.go`.

### Extension pattern

The daemon is small enough that forking is the extension mechanism. If you need custom behavior:
1. Copy `cmd/faktory-daemon/` to your repo
2. Import `github.com/sderosiaux/faktory`
3. Modify `server.go` and `policies.go`

No plugin interfaces. No hook system. The code IS the configuration.

---

## Dependencies

### Go modules

| Dependency | Purpose | Why this one |
|---|---|---|
| `github.com/sderosiaux/faktory` | Memory library | This project. The whole point. |
| `net/http` (stdlib) | HTTP server | No framework needed for ~10 routes. |
| `github.com/BurntSushi/toml` | Config parsing | Already used by faktory CLI. Small, stable. |
| `log/slog` (stdlib) | Structured logging | Already used by faktory. |

That's it. No web framework. No DI container. No ORM. No metrics library.

### Why no web framework

10 routes. Each handler is: parse JSON body → call `faktory.Memory` method → serialize response. A framework adds dependency weight for zero value. `net/http` + `encoding/json` is enough.

### Why no Prometheus / OpenTelemetry

Counters are `atomic.Int64` fields on a struct. `GET /v1/stats` serializes them to JSON. If someone needs Prometheus, they write a scraper that hits `/v1/stats` — or they fork and add `promhttp`. Not a core dependency.

### System dependencies

- Go 1.21+ (for slog)
- CGO enabled (for sqlite-vec, inherited from faktory)
- SQLite3 headers (inherited from faktory)

---

## Decisions

| Decision | Rationale | Reversible? |
|---|---|---|
| HTTP JSON API | Universal. Every language can call it. No gRPC complexity. | Yes (add gRPC later) |
| Single process | SQLite is single-writer. One daemon = one DB. Simple. | Hard (requires different storage) |
| TOML config | Matches faktory CLI. Humans read it. | Yes |
| All POST for writes | POST for mutations, GET for reads. REST-ish without being pedantic. | Yes |
| Shared API key auth | Single key, not per-agent. Enough for internal networks. Per-agent keys are v2. | Yes |
| Localhost default | Bind 127.0.0.1 unless explicitly configured. Security by default. | Yes |
| No agent identity | Namespace isolation is enough. Agent identity is a layer on top. | Yes |
| Counters not histograms | Simple integer counters. No Prometheus dependency. | Yes (add prom later) |
| No rate limiting | Single-process, trusted network. | Yes |

---

## Scope

### In (v1)

- HTTP API for all faktory operations (add, recall, search, prune, profile, facts, relations, delete)
- Shared API key auth (optional, recommended)
- Input validation on all endpoints (user_id, namespace, body size)
- Namespace-scoped write isolation
- Background prune policy on interval
- Background consolidation on threshold
- Health and stats endpoints
- TOML config file + env var overrides
- Structured logging (slog)
- Graceful shutdown (SIGTERM)

### Out (v1)

| Feature | Reason |
|---|---|
| Per-agent API keys | Shared key is enough for v1. Per-agent with namespace ACLs is v2. |
| Agent identity / attribution | Namespaces suffice. |
| Summary compaction | Needs design. Future policy. |
| Hierarchical consolidation | Research territory. |
| WebSocket / SSE | No streaming use case yet. |
| Admin UI | curl + jq is fine. |
| Clustering / replication | Single process by design. |
| OpenAPI spec generation | Write it by hand if needed. |

### Anti-Goals

- **Not a platform.** No user management, no billing, no multi-tenancy beyond namespace isolation.
- **Not a proxy.** Doesn't forward to other memory backends. It IS the memory backend.
- **Not clever.** No automatic policy tuning, no ML-based forgetting. Explicit config only.

---

## Data Model

None. Uses faktory's SQLite schema unchanged. The daemon is a wrapper, not a fork.

---

## File Structure

```
cmd/faktory-daemon/
├── main.go          # entry point, config loading, graceful shutdown
├── server.go        # HTTP handlers, router
├── policies.go      # background lifecycle goroutines
└── spec.md          # this file
```

~4 files. No internal packages. No interfaces. No middleware chains.

---

## Edge Cases

| Scenario | Behavior |
|---|---|
| Concurrent Add() from multiple HTTP clients | SQLite WAL serializes writes. Safe but sequential. Daemon holds one faktory.Memory instance. |
| Prune policy runs during Add() | SQLite handles this. Prune waits for Add's transaction to complete. |
| Config file missing | Fall back to env vars. Error if no LLM API key. |
| LLM API down | Add/Recall return HTTP 502 with the upstream error. Daemon stays up. |
| DB file locked by another process | Startup fails with clear error. One daemon per DB. |
| Very large prune (10K facts) | Runs in one transaction. SQLite handles this. May take seconds. |

---

## Success Criteria

### Functional

- `curl POST /v1/memory/add` stores facts and returns AddResult
- `curl POST /v1/memory/recall` returns facts + relations + summary
- Two separate `curl` processes can Add and Recall concurrently without corruption
- Prune policy runs on schedule and removes qualifying facts
- `GET /v1/health` returns 200 while daemon is running
- `GET /v1/stats` returns accurate counters
- SIGTERM triggers graceful shutdown (finish in-flight requests, stop policies)

### Quality

| Metric | Target |
|---|---|
| Startup with empty config | < 1 second |
| Add() HTTP latency overhead vs direct library | < 5ms |
| Binary size | < 50MB |
| Memory usage idle | < 30MB |
| LOC | < 1500 |

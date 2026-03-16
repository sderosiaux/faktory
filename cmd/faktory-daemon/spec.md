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
listen = ":8420"

[faktory]
db_path = "faktory.db"
llm_api_key = "sk-..."
llm_model = "gpt-4o-mini"
embed_model = "text-embedding-3-small"
embed_dimension = 256

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

### Policy: prune

Runs on interval. For each user+namespace, calls `faktory.Prune()` with the configured criteria. Logs what was pruned.

### Policy: consolidate

After each `Add()`, checks fact count. If above threshold, calls `Summarize()`. Same as `ConsolidateThreshold` in faktory but server-side.

### Policy: compact_summaries (future)

When a user has >N summaries, merge the oldest ones into a single summary. Requires an LLM call. Not in v1.

---

## Decisions

| Decision | Rationale | Reversible? |
|---|---|---|
| HTTP JSON API | Universal. Every language can call it. No gRPC complexity. | Yes (add gRPC later) |
| Single process | SQLite is single-writer. One daemon = one DB. Simple. | Hard (requires different storage) |
| TOML config | Matches faktory CLI. Humans read it. | Yes |
| All POST for writes | POST for mutations, GET for reads. REST-ish without being pedantic. | Yes |
| No auth | First version. Add API key auth if someone deploys it publicly. | Yes |
| No agent identity | Namespace isolation is enough. Agent identity is a layer on top. | Yes |
| Counters not histograms | Simple integer counters. No Prometheus dependency. | Yes (add prom later) |
| No rate limiting | Single-process, trusted network. | Yes |

---

## Scope

### In (v1)

- HTTP API for all faktory operations (add, recall, search, prune, profile, facts, relations, delete)
- Background prune policy on interval
- Background consolidation on threshold
- Health and stats endpoints
- TOML config file + env var overrides
- Structured logging (slog)
- Graceful shutdown (SIGTERM)

### Out (v1)

| Feature | Reason |
|---|---|
| Auth / API keys | Trusted network first. Add later. |
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

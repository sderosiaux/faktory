# Future: Memory OS Layer

## Context

From "Memory in the Age of AI Agents" (arXiv:2512.13564): as agents become autonomous, the memory system becomes the equivalent of a filesystem in an operating system. The paper argues the future agent stack is:

```
model
memory layer      ← faktory is here
tool layer
planner
environment
```

faktory today is a memory **library**. A Memory OS is a memory **runtime** — it manages lifecycle, access control, and coordination across multiple agents.

## Why This Matters

Today's limitations:
- **Single-agent only**: two agents can't safely share a faktory instance (SQLite single-writer)
- **No access control**: any caller with the Memory struct can read/write any user's data
- **No lifecycle policies**: pruning and consolidation are manual or threshold-based
- **No observability**: no metrics on memory health (growth rate, hit rate, staleness)

These don't matter for single-agent use. They block multi-agent deployments.

## What a Memory OS Would Add

### 1. Multi-Agent Shared Memory

Multiple agents read/write the same memory store with coordination:

```
Agent A (support) ──┐
Agent B (sales)   ──┤──→ Memory OS ──→ Storage
Agent C (ops)     ──┘
```

Each agent has an identity. Writes are attributed. Reads can be scoped by agent or shared.

**Technical path**: replace SQLite with a server mode (HTTP API or embedded PostgreSQL). Or use SQLite in WAL2 mode with connection pooling.

### 2. Access Control

Scoped permissions per agent:
- Agent A can read/write user facts for support namespace
- Agent B can read user facts but only write to sales namespace
- Agent C can read everything, write to ops namespace only

Not RBAC complexity — just namespace-level read/write permissions per agent identity.

### 3. Automatic Lifecycle Management

Policies that run without caller intervention:

```
policy "prune-stale" {
  trigger: daily
  action:  prune facts where age > 90d AND importance <= 2 AND access_count == 0
}

policy "consolidate" {
  trigger: fact_count > 200 per user
  action:  summarize oldest 50 facts into 5 higher-level facts, soft-delete originals
}

policy "compact-summaries" {
  trigger: summary_count > 10 per user
  action:  merge oldest summaries into a single summary
}
```

This is the "forgetting" mechanism the paper identifies as critical. Without it, memory grows until retrieval quality degrades.

### 4. Hierarchical Consolidation

Today: flat facts + session summaries. The paper describes a layered model:

```
Layer 0: raw facts (current)
Layer 1: session summaries (current)
Layer 2: topic clusters — "Alice's work history", "Alice's travel preferences"
Layer 3: user-level abstractions — the profile (current, but not layered)
```

Facts move up layers via summarization. Lower layers can be pruned after consolidation. Retrieval searches top-down: check Layer 2 first, drill into Layer 0 only if needed.

### 5. Memory Health Metrics

Observable signals:
- Facts per user (growth rate)
- Retrieval hit rate (% of recalls that return >0 results)
- Staleness distribution (how old are the facts being returned?)
- Contradiction rate (how often does reconciliation UPDATE/DELETE?)
- Token cost per Add() over time

Exposed via a `Stats()` method or Prometheus-style metrics endpoint.

## What's NOT in Scope

- **Distributed storage**: sharding across nodes. Premature.
- **Real-time sync**: event-driven memory updates across agents. Too complex.
- **Memory marketplace**: sharing memory schemas across projects. Not useful.
- **ML-based forgetting**: RL-trained forgetting policies. Research territory.

## Dependencies

Before Memory OS makes sense:
1. Prune() must be stable and battle-tested (implemented now)
2. Consolidation must work reliably (Summarize exists, auto-consolidate added)
3. Usage patterns from real deployments should inform the design
4. The single-agent use case must be solid first

## Key Insight from the Paper

> "Agents are not limited by reasoning. They are limited by memory."

The progression:
1. ~~LLM + tools~~ (current industry)
2. **LLM + tools + persistent memory** (faktory today)
3. LLM + tools + memory runtime (Memory OS)
4. LLM + tools + memory runtime + shared memory (multi-agent OS)

faktory is at step 2. This doc describes step 3. Step 4 requires real multi-agent deployment experience to design correctly.

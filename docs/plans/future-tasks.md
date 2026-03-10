# Future Tasks

## Custom Prompts

Let developers override the LLM prompts used for fact extraction, reconciliation, and entity extraction via Config fields. This enables domain-specific tuning without forking the library.

**Fields to add to Config:**
- `PromptFactExtraction string` — override the system prompt for extracting facts from conversation
- `PromptReconciliation string` — override the system prompt for reconciling new facts against existing ones
- `PromptEntityExtraction string` — override the system prompt for extracting entities and relations

**Behavior:** When set, use the custom prompt instead of the default. When empty, fall back to the hardcoded prompts in `prompts.go`.

**Scope:** Config-only change + prompt selection logic in `memory.go`. No new interfaces needed.

---

## Session / Namespace Scoping

Add optional `session_id` or `namespace` to isolate memories by conversation, project, or tenant. Currently all facts for a `user_id` live in a single flat space — this adds a second dimension of isolation.

**Use cases:**
- Customer support: isolate memories per ticket/conversation
- Multi-tenant SaaS: isolate memories per tenant
- Development: separate "work" vs "personal" memory spaces

**Design considerations:**
- Add optional `Namespace string` to Add/Recall/Search options (not Config — it's per-call)
- Default empty namespace = current behavior (no breaking change)
- Schema: add `namespace TEXT DEFAULT ''` column to facts, entities, relations tables
- Index on `(user_id, namespace)` for efficient queries
- Recall/Search filter by namespace when provided
- Export/Import respect namespace boundaries

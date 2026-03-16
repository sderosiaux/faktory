# Future Tasks

## ~~Custom Prompts~~ (done — R5)

Shipped. `PromptFactExtraction`, `PromptReconciliation`, `PromptEntityExtraction` in Config.

---

## ~~Session / Namespace Scoping~~ (done — R4)

Shipped. `WithNamespace()` per-call option on all methods.

---

## Memory OS

The memory layer becomes an agent runtime — managing lifecycle, access control, and multi-agent coordination.

**Why:** arXiv:2512.13564 argues that as agents become autonomous, the memory system becomes the equivalent of a filesystem in an OS. faktory today is a library (single-agent, single-process). A Memory OS handles what happens when multiple agents share memory, when memory grows unbounded, and when facts need access control.

**What it adds:**
1. **Multi-agent shared memory** — multiple agents read/write with attributed writes and coordination
2. **Access control** — namespace-level read/write permissions per agent identity
3. **Automatic lifecycle policies** — scheduled pruning, consolidation triggers, summary compaction
4. **Hierarchical consolidation** — facts → session summaries → topic clusters → user abstractions
5. **Memory health metrics** — growth rate, hit rate, staleness distribution, contradiction rate

**Prerequisites:**
- Prune() stable and battle-tested (done — R8)
- Auto-consolidation working (done — R8)
- Real deployment usage patterns to inform design

**Full spec:** `docs/plans/memory-os.md`

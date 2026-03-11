# Hyper-Edge Knowledge Graphs for Personal Memory Systems

Arxiv research survey conducted 2026-03-10. 10 papers analyzed across 7 search dimensions.

---

## Tier 1: Implement Now

### 1. PersonalAI: Hybrid Graph with Two Hyper-Edge Types
**[arXiv:2506.17001](https://arxiv.org/abs/2506.17001)** (June 2025, updated Feb 2026)

**What it does:** Personal memory framework with hybrid graph supporting standard edges plus two hyper-edge types: **thesis edges** (linking object vertices from the same source text into one atomic thought) and **episodic edges** (connecting all semantic vertices from a single dialogue turn). Compares A*, water-circle, beam search retrieval across 5 LLMs (Qwen2.5 7B, DeepSeek R1 7B, Llama3.1 8B, GPT4o-mini, DeepSeek V3).

**Why it matters for faktory:** Closest system to faktory's architecture. Thesis edges are "fact + qualifier context" — grouping multiple entities from one extraction into a richer unit. Episodic edges provide session-level grouping (similar to R6 summaries). Qwen2.5 7B produced the best thesis/object ratio with only 0.02% parse errors, suggesting small models can extract hyper-edges reliably. BeamSearch + WaterCircle retrieval scored 0.77 on DiaASQ.

**Key details:**
- Storage: Neo4j (graph) + Milvus (vectors) + Redis/MongoDB (caching)
- Parse error rates: DiaASQ 7.0%, HotpotQA 6.3%, TriviaQA 7.3%
- DeepSeek V3 highest errors (31.21%), Qwen2.5 7B lowest (0.02%)
- Best retrieval: BS+WC (BeamSearch + WaterCircles) = 0.77 on DiaASQ with GPT4o-mini

**Actionable:**
- Implement thesis edges as a `fact_qualifiers` table: `(fact_id, key, value)` linking qualifier pairs to facts
- Add episodic edges via the existing R6 summary mechanism
- Benchmark retrieval with DiaASQ dataset to compare faktory's Recall vs PersonalAI's 0.77

---

### 2. Construction of Hyper-Relational KGs Using Pre-Trained LLMs
**[arXiv:2403.11786](https://arxiv.org/abs/2403.11786)** (March 2024)

**What it does:** Zero-shot extraction of hyper-relational facts (triples + qualifier key:value pairs) from text using GPT-3.5 with ontology-grounded prompts. Tests against the HyperRED benchmark (62 relations, 44 qualifier types).

**Why it matters for faktory:** The only paper that directly measures LLM quality on qualifier extraction. The result is sobering: **precision 0.01 exact-match but recall 0.77 BERTScore**. GPT-3.5 captures qualifier information semantically but formats it inconsistently. GPT-4 achieved 98% consistency vs 36% for GPT-3.5. For gpt-4o-mini, expect moderate qualifier extraction quality — enough for temporal/source qualifiers but not fine-grained Wikidata-style qualifiers.

**Key details:**
- Method: zero-shot prompt with ontology integration (62 relation defs, 44 qualifier descriptions), chain-of-thought exemplar, key:value output format
- Results: Precision 0.01 (exact), Recall 0.77 (BERTScore) vs CubeRE baseline: P=0.62, R=0.66 (exact), R=0.88 (BERTScore)
- GPT-4 reproducibility: 98% consistency vs GPT-3.5's 36%
- Qualifiers stored as key:value pairs appended to relation triples

**Actionable:**
- Limit qualifier schema to 5-8 well-defined types, not 44
- Add qualifiers to extraction prompt as optional flat fields
- Use BERTScore-style fuzzy matching for qualifier values in reconciliation

---

### 3. Wikidata Reification Schema for Relational Databases
**[Hogan et al., ISWC 2016](https://aidanhogan.com/docs/wikidata-sparql-relational-graph.pdf)**

**What it does:** Compares SPARQL, PostgreSQL, and Neo4j for storing Wikidata's hyper-relational data. Winning relational schema: **Statement + Qualifier** two-table approach.

**Why it matters for faktory:** Proven pattern for storing qualifiers in relational DB. PostgreSQL outperformed graph databases for atomic lookups. The polymorphic value problem (dates, numbers, strings) is solved by storing all qualifier values as TEXT with a type hint column.

**Key details:**
- Schema: Statement(id, subject, predicate, object) + Qualifier(statement_id, key, value)
- PostgreSQL consistently outperforms others in atomic lookups
- Polymorphic values: either all-as-strings (lose type queries) or separate typed columns
- Reification introduces auxiliary nodes that inflate graph size

**Actionable:**
- For faktory: flat columns on facts table (not a separate table) since we have exactly 4 qualifier types
- Store temporal qualifiers in both existing Fact columns AND as qualifiers for consistency
- No embedding needed for qualifier values — they're metadata, not semantic content

---

## Tier 2: Inform Strategy

### 4. FormerGNN: Understanding Hyper-Relational KG Embedding Models
**[arXiv:2508.03280](https://arxiv.org/abs/2508.03280)** (August 2025, CIKM 2025)

**What it does:** Proves StarE (leading hyper-relational embedding model) suffers from "qualifier compression" — squishes qualifier info into fixed-size entity embeddings, losing signal. Simpler models (CompGCN) with decomposition achieve comparable results. Proposes FormerGNN which preserves qualifier topology.

**Why it matters for faktory:** Validates that **you don't need StarE embeddings**. Qualifier information is better stored as explicit metadata columns than compressed into vector embeddings. The compression actually degrades performance. faktory's current approach (separate columns for valid_from, importance, etc.) is architecturally sound.

**Key details:**
- CompGCN with hyper decomposition matches or beats StarE and QUAD
- StarE, QUAD, HAHE aggregate qualifiers into fixed-sized embeddings = noise compression
- FormerGNN: qualifier integrator preserves HKG topology + GNN graph encoder for long-range deps
- Accepted at CIKM 2025

**Actionable:**
- Do NOT invest in hyper-relational embedding models (StarE, HINGE)
- Keep qualifiers as explicit structured metadata, queryable via SQL
- If qualifier-aware similarity ever needed, concatenate key qualifiers into fact text before embedding

---

### 5. KGGen: Two-Step Entity-then-Relation Extraction
**[arXiv:2502.09956](https://arxiv.org/abs/2502.09956)** (February 2025, NeurIPS '25)

**What it does:** Two-step extraction (entities first, then relations) + iterative LLM-based entity clustering. Achieves 66% on MINE benchmark vs GraphRAG's 48%. Clustering resolves "NYC" = "New York City" type issues.

**Why it matters for faktory:** faktory already uses similar two-step approach. KGGen's 18% advantage over GraphRAG comes primarily from the **clustering step**, which is exactly what R4 (entity clustering) implements. Validates R4 design. Only tested with GPT-4o.

**Key details:**
- Step 1: GPT-4o extracts entities via DSPy signature
- Step 2: Second LLM call generates subject-predicate-object relations given entities + source text
- Clustering: iterative LLM-based, inspired by crowdsourcing entity resolution. LLM-as-judge validates each cluster.
- MINE benchmark (100 Wikipedia articles, 15 manually-verified facts each): KGGen 66.07%, GraphRAG 47.80%, OpenIE 29.84%
- Available as Python library (`pip install kg-gen`) with MCP server

**Actionable:**
- Compare R4's cosine-similarity clustering (threshold 0.6) against KGGen's LLM-based approach on MINE
- Consider LLM-as-judge validation step for R4 borderline cases (similarity 0.5-0.7)
- Port MINE benchmark as quality test for faktory's extraction pipeline

---

### 6. HOLMES: Hyper-Relational KGs for Multi-Hop QA
**[arXiv:2406.06027](https://arxiv.org/abs/2406.06027)** (June 2024, ACL 2024)

**What it does:** Constructs query-relevant distilled hyper-relational KGs for multi-hop QA. Uses **67% fewer tokens** than standard approaches while improving EM/F1/BERTScore on HotpotQA and MuSiQue.

**Why it matters for faktory:** The 67% token reduction comes from filtering the KG to only query-relevant subgraphs — exactly what Recall does (KNN + BFS expansion). Qualifiers like "time period" and "source" allow more aggressive filtering, reducing facts needed in Recall summary.

**Key details:**
- "Compressed distilled KG" tailored to specific questions
- Consistent improvements over SoTA across EM, F1, BERTScore, Human Eval
- Benchmarked on HotpotQA and MuSiQue (multi-hop QA)

**Actionable:**
- After implementing fact qualifiers, add qualifier-based filtering in Recall
- Measure token count in recall.Summary before/after qualifier-based filtering

---

### 7. Mem0: Production-Ready Agent Memory
**[arXiv:2504.19413](https://arxiv.org/abs/2504.19413)** (April 2025)

**What it does:** Production memory system with extract-reconcile pipeline (ADD/UPDATE/DELETE/NOOP) plus optional graph variant (Mem0g) using Neo4j. Achieves 66.9% accuracy at 0.20s median latency. Graph variant only adds +1.5%.

**Why it matters for faktory:** Architecture essentially identical to faktory's. **Mem0g only improves by 1.5% over flat Mem0.** Graph complexity provides marginal retrieval improvement for personal memory. Win is mostly in temporal and open-domain questions. Should temper expectations for hyper-edge ROI.

**Key details:**
- Extract phase: LLM extracts candidate facts from summary + last M turns
- Update phase: vector search for similar memories, LLM decides ADD/UPDATE/DELETE/NOOP
- Mem0: 66.9% accuracy, 0.20s median, 0.15s p95 latency
- Mem0g (Neo4j graph): 68.4% accuracy, 0.66s median, 0.48s p95
- Mem0g better only in Open-Domain and Temporal categories
- 91% lower p95 latency and 90%+ token savings vs standard RAG
- Benchmark: LOCOMO dataset

**Actionable:**
- Use LOCOMO benchmark to compare faktory's Recall accuracy against 66.9%
- Focus hyper-edge investment on temporal qualifiers (where Mem0g showed gains)

---

## Tier 3: Infrastructure

### 8. Generating Structured Outputs from LLMs: Benchmark
**[arXiv:2501.10868](https://arxiv.org/abs/2501.10868)** (January 2025)

**What it does:** Benchmarks 6 constrained decoding frameworks for JSON structured output. Tests Llama-3.1-8B on simple-to-complex schemas.

**Why it matters for faktory:** Adding qualifier fields to extraction JSON schema increases complexity. Simple schemas: 96% coverage. Complex nested schemas: 13-21%. For OpenAI's API (closed-source), compliance is high but coverage is lowest.

**Key details:**
- Frameworks: Guidance, Outlines, Llamacpp, XGrammar (open), OpenAI, Gemini (closed)
- Simple schemas (GlaiveAI): Guidance 96% coverage
- Complex schemas (GitHub Hard): Guidance drops to 41%
- Ultra-complex (JSONSchemaStore): 30% best
- Compliance: simple 94-98%, hard 41-69%
- Guidance: highest coverage 6/8 datasets, token healing +3% accuracy
- Constrained decoding 50% faster than unconstrained

**Actionable:**
- Keep qualifier schema flat (not nested arrays) to stay in the 90%+ coverage zone
- Test schema change on gpt-4o-mini with 100 samples before committing
- Fallback: if qualifier fields are empty/malformed, use fact without qualifiers

---

### 9. Text2NKG: Fine-Grained N-ary Relation Extraction
**[arXiv:2310.05185](https://arxiv.org/abs/2310.05185)** (NeurIPS 2024)

**What it does:** Span-tuple classification for extracting n-ary relations across 4 KG schemas (hyper-relational, event-based, role-based, hypergraph). SOTA F1 on fine-grained n-ary benchmarks.

**Why it matters for faktory:** Uses supervised span classification, not LLM prompting. For faktory (LLM structured output), this is informational: specialized models handle n-ary extraction well, but porting to Go requires embedding a Python model. Not practical for library-first positioning.

**Actionable:**
- Reference for quality ceiling — if faktory ever needs >90% qualifier extraction F1, specialized model needed
- The 4 schema types provide taxonomy for thinking about qualifier design

---

## Tier 4: Foundational

### 10. Memoria: Scalable Agentic Memory Framework
**[arXiv:2512.12686](https://arxiv.org/abs/2512.12686)** (December 2025)

**What it does:** Modular memory with weighted KG-based user modeling + dynamic session summarization. Captures user traits as weighted entities and relationships.

**Why it matters for faktory:** Validates R6 (session summaries) and profile generation approaches. "Weighted KG" concept (edge weights = confidence/frequency) is interesting but adds complexity without clear retrieval gains (same lesson as Mem0g).

**Actionable:**
- Consider adding `weight REAL DEFAULT 1.0` to relations table for future confidence scoring
- Monitor for benchmark releases to compare against

---

## Priority Matrix

| Paper | Impact | Effort | Priority |
|-------|--------|--------|----------|
| #1 PersonalAI (thesis/episodic edges) | High | Med | **P0** |
| #2 Hyper-Relational LLM Extraction | High | Low | **P0** |
| #3 Wikidata Reification Schema | High | Low | **P0** |
| #4 FormerGNN (don't use StarE) | Med | None | **P1** |
| #5 KGGen (clustering validation) | Med | Low | **P1** |
| #6 HOLMES (token reduction) | Med | Med | **P1** |
| #7 Mem0 (+1.5% graph ROI) | Med | Low | **P1** |
| #8 Structured Output Benchmark | Med | Low | **P2** |
| #9 Text2NKG (supervised, not applicable) | Low | High | **P3** |
| #10 Memoria (validates existing design) | Low | None | **P3** |

---

## Key Insight

Hyper-edges provide marginal retrieval improvement for personal memory systems. Mem0g's +1.5% over flat Mem0, PersonalAI's modest gains from thesis edges, and FormerGNN's finding that StarE's qualifier compression hurts — all point to the same conclusion. The value of hyper-edges is NOT in retrieval quality but in **structured metadata** (temporal validity, provenance, confidence) that enables **filtering** rather than ranking.

The practical path is a **qualifier metadata layer**: flat columns on the facts table, populated by adding optional fields to the extraction prompt, gated behind a config flag. Use qualifiers for SQL-level filtering in Recall (`WHERE valid_from > ?`, `WHERE confidence >= ?`) rather than embedding them into vectors.

---

## Benchmarks to Track

| Benchmark | What it measures | faktory baseline | Target |
|-----------|-----------------|-----------------|--------|
| LOCOMO (Mem0) | Multi-session QA accuracy | Unknown | >66.9% |
| DiaASQ (PersonalAI) | Dialogue QA with temporal annotations | Unknown | >0.77 |
| MINE (KGGen) | Extraction completeness from text | Unknown | >66% |
| HyperRED (2403.11786) | Qualifier extraction quality | N/A | >50% confidence set |

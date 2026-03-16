package faktory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// --- Recall (unified retrieval) ---

// Recall returns facts and relations relevant to a query in a single call.
// It embeds the query once, runs parallel KNN on facts and entity embeddings,
// expands relations via multi-hop BFS, and returns a pre-formatted summary.
func (m *Memory) Recall(ctx context.Context, query string, userID string, opts *RecallOptions) (*RecallResult, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}

	maxFacts, maxRels := 10, 10
	var ns string
	if opts != nil {
		if opts.MaxFacts > 0 {
			maxFacts = opts.MaxFacts
		}
		if opts.MaxRelations > 0 {
			maxRels = opts.MaxRelations
		}
		ns = opts.Namespace
	}

	emb, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	var (
		facts     []Fact
		entityIDs []string
		factErr   error
		relErr    error
		wg        sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		facts, factErr = m.store.SearchFacts(emb, userID, ns, maxFacts*2)
	}()
	go func() {
		defer wg.Done()
		entityIDs, relErr = m.store.SearchEntityIDs(emb, userID, ns, 10, 0.5)
	}()
	wg.Wait()

	// Expand matched entities to their clusters before BFS
	if len(entityIDs) > 0 {
		clusterIDs, clErr := m.store.GetClusterEntityIDs(entityIDs, userID, ns)
		if clErr == nil && len(clusterIDs) > len(entityIDs) {
			entityIDs = clusterIDs
		}
	}

	if factErr != nil {
		return nil, fmt.Errorf("search facts: %w", factErr)
	}
	if relErr != nil {
		return nil, fmt.Errorf("search entity IDs: %w", relErr)
	}

	// Qualifier-based filtering
	if opts != nil && opts.MinConfidence > 0 {
		var filtered []Fact
		for _, f := range facts {
			if f.Confidence >= opts.MinConfidence {
				filtered = append(filtered, f)
			}
		}
		facts = filtered
	}

	applyDecay(facts, m.cfg.DecayAlpha, m.cfg.DecayBeta)
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}

	if opts != nil && opts.Rerank && len(facts) > 0 {
		facts, _ = m.rerankFacts(ctx, query, facts)
	}

	factIDs := make([]string, len(facts))
	for i, f := range facts {
		factIDs[i] = f.ID
	}
	_ = m.store.BumpAccess(factIDs)

	expandLimit := maxRels
	if expandLimit > 20 {
		expandLimit = 20
	}
	rels, err := m.store.ExpandRelations(entityIDs, userID, ns, 2, expandLimit)
	if err != nil {
		return nil, fmt.Errorf("expand relations: %w", err)
	}

	// Pre-format summary
	var sb strings.Builder

	if opts != nil && opts.IncludeProfile {
		profile, err := m.Profile(ctx, userID, WithNamespace(ns))
		if err != nil {
			m.log.Warn("profile generation error", "err", err)
		} else if profile != "" {
			sb.WriteString("User profile:\n")
			sb.WriteString(profile)
			sb.WriteString("\n\n")
		}
	}

	if len(facts) > 0 {
		sb.WriteString("Relevant facts:\n")
		for _, f := range facts {
			sb.WriteString("- ")
			sb.WriteString(f.Text)
			if f.Confidence > 0 || f.Source != "" {
				sb.WriteString(" (")
				wrote := false
				if f.Confidence > 0 {
					fmt.Fprintf(&sb, "confidence: %d", f.Confidence)
					wrote = true
				}
				if f.Source != "" {
					if wrote {
						sb.WriteString(", ")
					}
					fmt.Fprintf(&sb, "source: %s", f.Source)
				}
				sb.WriteString(")")
			}
			sb.WriteString("\n")
		}
	}
	if len(rels) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Relationships:\n")
		for _, r := range rels {
			fmt.Fprintf(&sb, "- %s --%s--> %s\n", r.Source, r.Relation, r.Target)
		}
	}

	// Append session summaries
	if summaries, err := m.store.GetSummaries(userID, ns, 3); err == nil && len(summaries) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Session summaries:\n")
		for _, s := range summaries {
			sb.WriteString("- ")
			sb.WriteString(s.Text)
			sb.WriteString("\n")
		}
	}

	return &RecallResult{
		Facts:     facts,
		Relations: rels,
		Summary:   sb.String(),
	}, nil
}

// rerankFacts asks the LLM to reorder facts by relevance to query.
// On error, it silently returns the original order.
func (m *Memory) rerankFacts(ctx context.Context, query string, facts []Fact) ([]Fact, error) {
	if len(facts) == 0 {
		return facts, nil
	}

	var sb strings.Builder
	sb.WriteString("Query: ")
	sb.WriteString(query)
	sb.WriteString("\n\nFacts:\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "- [%s] %s\n", f.ID, f.Text)
	}

	var result RerankResult
	_, err := m.llm.Complete(ctx, rerankPrompt, sb.String(), "rerank", rerankSchema, &result)
	if err != nil {
		m.log.Warn("rerank LLM error, using original order", "err", err)
		return facts, nil
	}

	factByID := make(map[string]Fact, len(facts))
	for _, f := range facts {
		factByID[f.ID] = f
	}

	var reranked []Fact
	seen := make(map[string]bool)
	for _, id := range result.RankedIDs {
		if f, ok := factByID[id]; ok && !seen[id] {
			reranked = append(reranked, f)
			seen[id] = true
		}
	}
	// Append any facts the LLM missed (safety net)
	for _, f := range facts {
		if !seen[f.ID] {
			reranked = append(reranked, f)
		}
	}

	return reranked, nil
}

// --- Profile ---

// Profile generates a concise user summary from all stored facts and relations.
// The result is cached and only regenerated when facts change.
func (m *Memory) Profile(ctx context.Context, userID string, opts ...Option) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	facts, err := m.store.GetAllFacts(userID, o.namespace, 200)
	if err != nil {
		return "", fmt.Errorf("get facts: %w", err)
	}
	if len(facts) == 0 {
		return "", nil
	}

	currentHash := profileFactHash(facts)

	cached, storedHash, err := m.store.GetProfile(userID, o.namespace)
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	if cached != "" && storedHash == currentHash {
		return cached, nil
	}

	rels, err := m.store.GetAllRelations(userID, o.namespace, 100)
	if err != nil {
		return "", fmt.Errorf("get relations: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("Facts:\n")
	for _, f := range facts {
		sb.WriteString("- ")
		sb.WriteString(f.Text)
		sb.WriteString("\n")
	}
	if len(rels) > 0 {
		sb.WriteString("\nRelationships:\n")
		for _, r := range rels {
			fmt.Fprintf(&sb, "- %s --%s--> %s\n", r.Source, r.Relation, r.Target)
		}
	}

	type profileResult struct {
		Profile string `json:"profile"`
	}
	var result profileResult
	_, err = m.llm.Complete(ctx, profileGenerationPrompt, sb.String(), "profile", profileSchema, &result)
	if err != nil {
		return "", fmt.Errorf("generate profile: %w", err)
	}

	_ = m.store.UpsertProfile(userID, o.namespace, result.Profile, currentHash)

	return result.Profile, nil
}

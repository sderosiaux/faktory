package faktory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Memory is the main API for faktory.
type Memory struct {
	store    *Store
	llm      Completer
	embedder TextEmbedder
	cfg      Config
	log      *slog.Logger
}

// New creates a new Memory instance from config.
func New(cfg Config) (*Memory, error) {
	cfg = cfg.withDefaults()

	store, err := OpenStore(cfg.DBPath, cfg.EmbedDimension)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	var llm Completer
	if cfg.Completer != nil {
		llm = cfg.Completer
	} else {
		httpClient := cfg.buildHTTPClient()
		llm = NewLLM(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, httpClient)
	}

	var embedder TextEmbedder
	if cfg.TextEmbedder != nil {
		embedder = cfg.TextEmbedder
	} else {
		httpClient := cfg.buildHTTPClient()
		embedder = NewEmbedder(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.EmbedModel, cfg.EmbedDimension, httpClient)
	}

	return &Memory{
		store:    store,
		llm:      llm,
		embedder: embedder,
		cfg:      cfg,
		log:      cfg.Logger,
	}, nil
}

// Close releases all resources.
func (m *Memory) Close() error {
	return m.store.Close()
}

// Add extracts facts and entities from messages, reconciles with existing facts, and stores them.
// Skips processing if the exact same conversation was already processed for this user.
func (m *Memory) Add(ctx context.Context, messages []Message, userID string, opts ...Option) (*AddResult, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	// Conversation-level dedup: skip if exact same messages already processed
	contentHash := hashFact(formatMessages(messages))
	if exists, err := m.store.ConversationExists(userID, o.namespace, contentHash); err == nil && exists {
		return &AddResult{}, nil
	}

	var (
		factResult     *AddResult
		factErr        error
		graphTokens    int
		graphEntities  []EntityRef
		graphRelations []RelationRef
		graphErr       error
		wg             sync.WaitGroup
	)

	wg.Add(1)

	// Fact pipeline
	go func() {
		defer wg.Done()
		factResult, factErr = m.addFacts(ctx, messages, userID, o.namespace)
	}()

	// Graph pipeline (non-fatal)
	if !m.cfg.DisableGraph {
		wg.Add(1)
		go func() {
			defer wg.Done()
			graphTokens, graphEntities, graphRelations, graphErr = m.addGraph(ctx, messages, userID, o.namespace)
		}()
	}

	wg.Wait()

	if graphErr != nil {
		m.log.Warn("graph pipeline error", "err", graphErr)
	}

	if factErr != nil {
		return nil, fmt.Errorf("fact pipeline: %w", factErr)
	}

	if graphErr != nil {
		factResult.GraphErrors = append(factResult.GraphErrors, graphErr.Error())
	}

	factResult.ExtractedEntities = graphEntities
	factResult.ExtractedRelations = graphRelations

	// Mark conversation as processed for future dedup
	_ = m.store.MarkConversationProcessed(userID, o.namespace, contentHash)

	factResult.Tokens += graphTokens

	if count, err := m.store.CountFacts(userID, o.namespace); err == nil {
		factResult.TotalFacts = count
	}

	return factResult, nil
}

// --- Fact Pipeline ---

func (m *Memory) addFacts(ctx context.Context, messages []Message, userID, namespace string) (*AddResult, error) {
	messages = truncateMessages(m.log, messages, maxMessageChars)
	userContent := formatMessages(messages)

	totalTokens := 0

	factPrompt := factExtractionPrompt
	if m.cfg.PromptFactExtraction != "" {
		factPrompt = m.cfg.PromptFactExtraction
	}

	var extraction FactExtractionResult
	tokens, err := m.llm.Complete(ctx, factPrompt, userContent, "fact_extraction", factExtractionSchema, &extraction)
	totalTokens += tokens
	if err != nil {
		return nil, fmt.Errorf("extract facts: %w", err)
	}

	if len(extraction.Facts) == 0 {
		return &AddResult{Tokens: totalTokens}, nil
	}

	// Build importance map from extraction
	importanceMap := make(map[string]int, len(extraction.Facts))
	for _, ef := range extraction.Facts {
		importanceMap[ef.Text] = ef.Importance
	}

	// Hash-filter extracted facts
	var textsToEmbed []string
	var hashes []string
	for _, ef := range extraction.Facts {
		h := hashFact(ef.Text)
		exists, err := m.store.FactExistsByHash(userID, namespace, h)
		if err != nil {
			return nil, fmt.Errorf("check hash: %w", err)
		}
		if exists {
			continue
		}
		textsToEmbed = append(textsToEmbed, ef.Text)
		hashes = append(hashes, h)
	}

	if len(textsToEmbed) == 0 {
		return &AddResult{Tokens: totalTokens}, nil
	}

	// Batch embed all non-duplicate facts
	embeddings, err := m.embedder.EmbedBatch(ctx, textsToEmbed)
	if err != nil {
		return nil, fmt.Errorf("embed facts: %w", err)
	}

	// Search similar for each embedded fact
	var candidates []candidateFact
	for i, factText := range textsToEmbed {
		similar, err := m.store.SearchFacts(embeddings[i], userID, namespace, 5)
		if err != nil {
			return nil, fmt.Errorf("search similar: %w", err)
		}
		var filtered []Fact
		for _, f := range similar {
			if f.Score >= 0.5 {
				filtered = append(filtered, f)
			}
		}
		candidates = append(candidates, candidateFact{
			text:       factText,
			hash:       hashes[i],
			embedding:  embeddings[i],
			similar:    filtered,
			importance: importanceMap[factText],
		})
	}

	// Build embedding map for all candidates (needed for both novel and reconciled paths)
	candidateEmbs := make(map[string][]float32, len(candidates))
	for _, c := range candidates {
		candidateEmbs[c.text] = c.embedding
	}

	// Separate novel facts (no similar existing) from those needing reconciliation
	result := &AddResult{}
	var needsReconciliation []candidateFact
	for _, c := range candidates {
		if len(c.similar) == 0 {
			// Novel fact: directly insert without reconciliation
			id, err := m.store.InsertFact(userID, namespace, c.text, c.hash, c.embedding, c.importance)
			if err != nil {
				return nil, fmt.Errorf("insert novel fact: %w", err)
			}
			result.Added = append(result.Added, Fact{ID: id, Text: c.text, UserID: userID})
		} else {
			needsReconciliation = append(needsReconciliation, c)
		}
	}

	var deletedTexts []string

	// Reconcile candidates in chunks to avoid oversized LLM prompts
	if len(needsReconciliation) > 0 {
		for i := 0; i < len(needsReconciliation); i += maxReconcileChunk {
			end := i + maxReconcileChunk
			if end > len(needsReconciliation) {
				end = len(needsReconciliation)
			}
			cr, err := m.reconcileChunk(ctx, needsReconciliation[i:end], candidateEmbs, userID, namespace)
			if err != nil {
				return nil, err
			}
			result.Added = append(result.Added, cr.added...)
			result.Updated = append(result.Updated, cr.updated...)
			result.Deleted = append(result.Deleted, cr.deleted...)
			deletedTexts = append(deletedTexts, cr.deletedTexts...)
			result.Noops += cr.noops
			totalTokens += cr.tokens
		}
	}

	// Cleanup stale relations after fact updates/deletes
	if len(deletedTexts) > 0 {
		if cleaned, err := m.store.CleanupStaleRelations(userID, namespace, deletedTexts); err != nil {
			m.log.Warn("stale relation cleanup error", "err", err)
		} else if cleaned > 0 {
			m.log.Info("cleaned stale relations", "count", cleaned)
		}
	}

	extractedTexts := make([]string, len(extraction.Facts))
	for i, ef := range extraction.Facts {
		extractedTexts[i] = ef.Text
	}
	result.ExtractedFacts = extractedTexts
	result.Tokens = totalTokens
	return result, nil
}

// --- Graph Pipeline ---

func (m *Memory) addGraph(ctx context.Context, messages []Message, userID, namespace string) (int, []EntityRef, []RelationRef, error) {
	messages = truncateMessages(m.log, messages, maxMessageChars)
	userContent := formatUserMessages(messages)

	entPrompt := entityExtractionPrompt
	if m.cfg.PromptEntityExtraction != "" {
		entPrompt = m.cfg.PromptEntityExtraction
	}

	var extraction EntityExtractionResult
	tokens, err := m.llm.Complete(ctx, entPrompt, userContent, "entity_extraction", entityExtractionSchema, &extraction)
	if err != nil {
		return tokens, nil, nil, fmt.Errorf("extract entities: %w", err)
	}

	// Validate extraction — retry once on errors, log warnings
	issues := validateExtraction(&extraction)
	if len(issues.warnings) > 0 {
		m.log.Warn("entity extraction warnings", "issues", strings.Join(issues.warnings, "; "))
	}
	if len(issues.errors) > 0 {
		m.log.Warn("entity extraction errors, requesting correction", "count", len(issues.errors), "errors", strings.Join(issues.errors, "; "))

		previousJSON, _ := json.Marshal(extraction)
		correction := fmt.Sprintf(
			"Your previous extraction has %d issues:\n- %s\n\nPlease fix ALL issues and return the corrected extraction.",
			len(issues.errors), strings.Join(issues.errors, "\n- "),
		)

		var corrected EntityExtractionResult
		retryTokens, retryErr := m.llm.CompleteWithCorrection(
			ctx, entPrompt, userContent, string(previousJSON), correction,
			"entity_extraction", entityExtractionSchema, &corrected,
		)
		tokens += retryTokens

		if retryErr == nil {
			newIssues := validateExtraction(&corrected)
			if len(newIssues.errors) < len(issues.errors) {
				extraction = corrected
			}
			if len(newIssues.errors) > 0 {
				m.log.Warn("correction still has errors", "remaining", len(newIssues.errors), "original", len(issues.errors))
			}
		} else {
			m.log.Warn("correction request failed, using original", "err", retryErr)
		}
	}

	// Collect extracted refs for observability
	var entityRefs []EntityRef
	var relationRefs []RelationRef
	for _, e := range extraction.Entities {
		entityRefs = append(entityRefs, EntityRef(e))
	}
	for _, r := range extraction.Relations {
		relationRefs = append(relationRefs, RelationRef(r))
	}

	// Upsert entities
	entityKeyToID := make(map[string]string)
	var entNames []string
	var entIDs []string
	for _, e := range extraction.Entities {
		name := strings.TrimSpace(e.Name)
		if isPronoun(name) || name == "" {
			continue
		}

		id, err := m.store.UpsertEntity(userID, namespace, name, e.Type)
		if err != nil {
			return tokens, nil, nil, fmt.Errorf("upsert entity %q: %w", name, err)
		}
		entityKeyToID[entityKey(name)] = id
		entNames = append(entNames, name)
		entIDs = append(entIDs, id)
	}

	// Batch embed all entity names
	if len(entNames) > 0 {
		entEmbs, err := m.embedder.EmbedBatch(ctx, entNames)
		if err != nil {
			return tokens, nil, nil, fmt.Errorf("embed entities: %w", err)
		}
		for i, id := range entIDs {
			if err := m.store.UpsertEntityEmbedding(id, entEmbs[i]); err != nil {
				return tokens, nil, nil, fmt.Errorf("store entity embedding %q: %w", entNames[i], err)
			}
		}
	}

	// Upsert relations
	for _, r := range extraction.Relations {
		source := strings.TrimSpace(r.Source)
		target := strings.TrimSpace(r.Target)
		if isPronoun(source) || isPronoun(target) {
			continue
		}

		sourceID, ok := entityKeyToID[entityKey(source)]
		if !ok {
			id, err := m.store.UpsertEntity(userID, namespace, source, "other")
			if err != nil {
				return tokens, nil, nil, fmt.Errorf("upsert source entity %q: %w", source, err)
			}
			sourceID = id
			entityKeyToID[entityKey(source)] = id
		}

		targetID, ok := entityKeyToID[entityKey(target)]
		if !ok {
			id, err := m.store.UpsertEntity(userID, namespace, target, "other")
			if err != nil {
				return tokens, nil, nil, fmt.Errorf("upsert target entity %q: %w", target, err)
			}
			targetID = id
			entityKeyToID[entityKey(target)] = id
		}

		if err := m.store.UpsertRelation(userID, namespace, sourceID, r.Relation, targetID); err != nil {
			return tokens, nil, nil, fmt.Errorf("upsert relation: %w", err)
		}
	}

	return tokens, entityRefs, relationRefs, nil
}

// --- Read Path ---

// Search finds facts similar to the query for a given user.
// Results are re-ranked with temporal decay scoring and access counts are bumped.
func (m *Memory) Search(ctx context.Context, query string, userID string, limit int, opts ...Option) ([]Fact, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	o := resolveOpts(opts)

	emb, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	facts, err := m.store.SearchFacts(emb, userID, o.namespace, limit*2)
	if err != nil {
		return nil, err
	}

	// BM25 hybrid: fuse vector results with full-text keyword matches
	bm25Facts, _ := m.store.SearchFactsBM25(query, userID, o.namespace, limit*2)
	if len(bm25Facts) > 0 {
		facts = fuseScores(facts, bm25Facts, m.cfg.BM25Weight)
	}

	applyDecay(facts, m.cfg.DecayAlpha, m.cfg.DecayBeta)
	if len(facts) > limit {
		facts = facts[:limit]
	}

	ids := make([]string, len(facts))
	for i, f := range facts {
		ids[i] = f.ID
	}
	_ = m.store.BumpAccess(ids)

	return facts, nil
}

// Get retrieves a single fact by ID.
func (m *Memory) Get(ctx context.Context, factID string) (*Fact, error) {
	return m.store.GetFact(factID)
}

// GetAll retrieves all facts for a user.
func (m *Memory) GetAll(ctx context.Context, userID string, limit int, opts ...Option) ([]Fact, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	o := resolveOpts(opts)
	return m.store.GetAllFacts(userID, o.namespace, limit)
}

// Update manually updates a fact's text, re-embeds it.
func (m *Memory) Update(ctx context.Context, factID string, text string) error {
	emb, err := m.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed updated text: %w", err)
	}
	_, err = m.store.UpdateFact(factID, text, hashFact(text), emb)
	return err
}

// Delete removes a single fact and its embedding.
func (m *Memory) Delete(ctx context.Context, factID string) error {
	return m.store.DeleteFact(factID)
}

// History returns all history entries for a fact, newest first.
func (m *Memory) History(_ context.Context, factID string) ([]FactHistoryEntry, error) {
	return m.store.GetFactHistory(factID)
}

// HistoryAt returns facts that were valid at the given point in time.
func (m *Memory) HistoryAt(_ context.Context, userID string, at time.Time, opts ...Option) ([]Fact, error) {
	o := resolveOpts(opts)
	return m.store.GetFactsAt(userID, o.namespace, at, 200)
}

// Undo reverses the latest mutation on a fact.
//   - DELETE -> re-insert fact with old_text, re-embed, record UNDO
//   - UPDATE -> restore old_text, re-embed, update hash, record UNDO
//   - ADD    -> delete the fact, record UNDO
func (m *Memory) Undo(ctx context.Context, factID string) error {
	entry, err := m.store.GetLatestHistoryEntry(factID)
	if err != nil {
		return fmt.Errorf("get latest history: %w", err)
	}
	if entry == nil {
		return fmt.Errorf("no history for fact %s", factID)
	}

	switch entry.Event {
	case "DELETE":
		emb, err := m.embedder.Embed(ctx, entry.OldText)
		if err != nil {
			return fmt.Errorf("embed restored text: %w", err)
		}
		if err := m.store.ReinsertFact(factID, entry.UserID, entry.OldText, hashFact(entry.OldText), emb, 3); err != nil {
			return fmt.Errorf("reinsert fact: %w", err)
		}

	case "UPDATE":
		emb, err := m.embedder.Embed(ctx, entry.OldText)
		if err != nil {
			return fmt.Errorf("embed restored text: %w", err)
		}
		if _, err := m.store.UpdateFact(factID, entry.OldText, hashFact(entry.OldText), emb); err != nil {
			return fmt.Errorf("restore fact: %w", err)
		}

	case "ADD":
		if err := m.store.DeleteFact(factID); err != nil {
			return fmt.Errorf("undo add: %w", err)
		}

	default:
		return fmt.Errorf("cannot undo event type %q", entry.Event)
	}

	// Record UNDO history entry
	if _, err := m.store.db.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, event, old_text, new_text, created_at) VALUES (?, ?, ?, 'UNDO', ?, ?, ?)",
		newID(), factID, entry.UserID, entry.NewText, entry.OldText, now()); err != nil {
		return fmt.Errorf("record undo history: %w", err)
	}

	return nil
}

// PruneHistory deletes history entries older than the given duration for a user.
func (m *Memory) PruneHistory(_ context.Context, userID string, olderThan time.Duration, opts ...Option) (int, error) {
	o := resolveOpts(opts)
	cutoff := time.Now().Add(-olderThan)
	return m.store.PruneHistoryForUser(userID, o.namespace, cutoff)
}

// DeleteAll removes all data (facts, embeddings, entities, relations) for a user.
func (m *Memory) DeleteAll(ctx context.Context, userID string, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)
	return m.store.DeleteAllForUser(userID, o.namespace)
}

// SearchRelations finds relations matching a query for a user via entity embedding similarity.
func (m *Memory) SearchRelations(ctx context.Context, query string, userID string, limit int, opts ...Option) ([]Relation, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	o := resolveOpts(opts)
	emb, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return m.store.SearchRelations(emb, userID, o.namespace, limit)
}

// GetAllRelations retrieves all relations for a user.
func (m *Memory) GetAllRelations(ctx context.Context, userID string, limit int, opts ...Option) ([]Relation, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	o := resolveOpts(opts)
	return m.store.GetAllRelations(userID, o.namespace, limit)
}

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

	if factErr != nil {
		return nil, fmt.Errorf("search facts: %w", factErr)
	}
	if relErr != nil {
		return nil, fmt.Errorf("search entity IDs: %w", relErr)
	}

	applyDecay(facts, m.cfg.DecayAlpha, m.cfg.DecayBeta)
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
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

	return &RecallResult{
		Facts:     facts,
		Relations: rels,
		Summary:   sb.String(),
	}, nil
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

// --- Import / Export ---

// Export writes all facts, entities, and relations for a user as JSONL.
func (m *Memory) Export(ctx context.Context, userID string, w io.Writer, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	facts, err := m.store.GetAllFacts(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export facts: %w", err)
	}
	for _, f := range facts {
		if err := enc.Encode(ExportRecord{Type: "fact", Text: f.Text}); err != nil {
			return err
		}
	}

	entities, err := m.store.GetAllEntities(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export entities: %w", err)
	}
	for _, e := range entities {
		if err := enc.Encode(ExportRecord{Type: "entity", Name: e.Name, EntityType: e.Type}); err != nil {
			return err
		}
	}

	rels, err := m.store.GetAllRelations(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export relations: %w", err)
	}
	for _, r := range rels {
		if err := enc.Encode(ExportRecord{Type: "relation", Source: r.Source, Relation: r.Relation, Target: r.Target}); err != nil {
			return err
		}
	}

	return nil
}

// Import reads JSONL records and inserts them for a user. Facts and entities
// are embedded on import. Existing data is not cleared first.
func (m *Memory) Import(ctx context.Context, userID string, r io.Reader, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var factTexts []string
	var entityRecords []ExportRecord
	var relationRecords []ExportRecord

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec ExportRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("parse record: %w", err)
		}
		switch rec.Type {
		case "fact":
			factTexts = append(factTexts, rec.Text)
		case "entity":
			entityRecords = append(entityRecords, rec)
		case "relation":
			relationRecords = append(relationRecords, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	if len(factTexts) > 0 {
		embs, err := m.embedder.EmbedBatch(ctx, factTexts)
		if err != nil {
			return fmt.Errorf("embed facts: %w", err)
		}
		for i, text := range factTexts {
			if _, err := m.store.InsertFact(userID, o.namespace, text, hashFact(text), embs[i], 3); err != nil {
				return fmt.Errorf("insert fact: %w", err)
			}
		}
	}

	if len(entityRecords) > 0 {
		names := make([]string, len(entityRecords))
		for i, rec := range entityRecords {
			names[i] = rec.Name
		}
		embs, err := m.embedder.EmbedBatch(ctx, names)
		if err != nil {
			return fmt.Errorf("embed entities: %w", err)
		}
		for i, rec := range entityRecords {
			id, err := m.store.UpsertEntity(userID, o.namespace, rec.Name, rec.EntityType)
			if err != nil {
				return fmt.Errorf("upsert entity: %w", err)
			}
			if err := m.store.UpsertEntityEmbedding(id, embs[i]); err != nil {
				return fmt.Errorf("store entity embedding: %w", err)
			}
		}
	}

	for _, rec := range relationRecords {
		srcID, err := m.store.UpsertEntity(userID, o.namespace, rec.Source, "other")
		if err != nil {
			return fmt.Errorf("upsert source: %w", err)
		}
		tgtID, err := m.store.UpsertEntity(userID, o.namespace, rec.Target, "other")
		if err != nil {
			return fmt.Errorf("upsert target: %w", err)
		}
		if err := m.store.UpsertRelation(userID, o.namespace, srcID, rec.Relation, tgtID); err != nil {
			return fmt.Errorf("upsert relation: %w", err)
		}
	}

	return nil
}

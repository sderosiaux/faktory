package faktory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
)

// Memory is the main API for faktory.
type Memory struct {
	store    *Store
	llm      *LLM
	embedder *Embedder
	cfg      Config
}

// New creates a new Memory instance from config.
func New(cfg Config) (*Memory, error) {
	cfg = cfg.withDefaults()

	store, err := OpenStore(cfg.DBPath, cfg.EmbedDimension)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	llm := NewLLM(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
	embedder := NewEmbedder(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.EmbedModel, cfg.EmbedDimension)

	return &Memory{
		store:    store,
		llm:      llm,
		embedder: embedder,
		cfg:      cfg,
	}, nil
}

// Close releases all resources.
func (m *Memory) Close() error {
	return m.store.Close()
}

// Add extracts facts and entities from messages, reconciles with existing facts, and stores them.
// Skips processing if the exact same conversation was already processed for this user.
func (m *Memory) Add(ctx context.Context, messages []Message, userID string) (*AddResult, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}

	// Conversation-level dedup: skip if exact same messages already processed
	contentHash := hashFact(formatMessages(messages))
	if exists, err := m.store.ConversationExists(userID, contentHash); err == nil && exists {
		return &AddResult{}, nil
	}

	var (
		factResult  *AddResult
		factErr     error
		graphTokens int
		graphErr    error
		wg          sync.WaitGroup
	)

	wg.Add(2)

	// Fact pipeline
	go func() {
		defer wg.Done()
		factResult, factErr = m.addFacts(ctx, messages, userID)
	}()

	// Graph pipeline (non-fatal)
	go func() {
		defer wg.Done()
		graphTokens, graphErr = m.addGraph(ctx, messages, userID)
	}()

	wg.Wait()

	if graphErr != nil {
		log.Printf("graph pipeline error (non-fatal): %v", graphErr)
	}

	if factErr != nil {
		return nil, fmt.Errorf("fact pipeline: %w", factErr)
	}

	// Mark conversation as processed for future dedup
	_ = m.store.MarkConversationProcessed(userID, contentHash)

	factResult.Tokens += graphTokens
	return factResult, nil
}

// --- Fact Pipeline ---

func (m *Memory) addFacts(ctx context.Context, messages []Message, userID string) (*AddResult, error) {
	messages = truncateMessages(messages, maxMessageChars)
	userContent := formatMessages(messages)

	totalTokens := 0

	var extraction FactExtractionResult
	tokens, err := m.llm.Complete(ctx, factExtractionPrompt, userContent, "fact_extraction", factExtractionSchema, &extraction)
	totalTokens += tokens
	if err != nil {
		return nil, fmt.Errorf("extract facts: %w", err)
	}

	if len(extraction.Facts) == 0 {
		return &AddResult{Tokens: totalTokens}, nil
	}

	// Hash-filter extracted facts
	type candidateFact struct {
		text      string
		hash      string
		embedding []float32
		similar   []Fact
	}

	var textsToEmbed []string
	var hashes []string
	for _, factText := range extraction.Facts {
		h := hashFact(factText)
		exists, err := m.store.FactExistsByHash(userID, h)
		if err != nil {
			return nil, fmt.Errorf("check hash: %w", err)
		}
		if exists {
			continue
		}
		textsToEmbed = append(textsToEmbed, factText)
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
		similar, err := m.store.SearchFacts(embeddings[i], userID, 5)
		if err != nil {
			return nil, fmt.Errorf("search similar: %w", err)
		}
		candidates = append(candidates, candidateFact{
			text:      factText,
			hash:      hashes[i],
			embedding: embeddings[i],
			similar:   similar,
		})
	}

	// Collect all similar existing facts, deduplicate (stable order)
	existingByID := make(map[string]Fact)
	var existingOrder []string
	for _, c := range candidates {
		for _, f := range c.similar {
			if _, seen := existingByID[f.ID]; !seen {
				existingByID[f.ID] = f
				existingOrder = append(existingOrder, f.ID)
			}
		}
	}

	// Map UUIDs to sequential integers (deterministic via existingOrder)
	idToInt := make(map[string]string)
	intToID := make(map[string]string)
	for idx, id := range existingOrder {
		intStr := fmt.Sprintf("%d", idx)
		idToInt[id] = intStr
		intToID[intStr] = id
	}

	var existingLines []string
	for _, id := range existingOrder {
		f := existingByID[id]
		existingLines = append(existingLines, fmt.Sprintf("id: %s, text: %s", idToInt[id], f.Text))
	}

	var newLines []string
	for _, c := range candidates {
		newLines = append(newLines, c.text)
	}

	reconcileInput := fmt.Sprintf("Existing facts:\n%s\n\nNew facts:\n%s",
		strings.Join(existingLines, "\n"),
		strings.Join(newLines, "\n"))

	// Reconcile via LLM
	var reconciliation ReconcileResult
	tokens, err = m.llm.Complete(ctx, reconcilePrompt, reconcileInput, "reconcile_memory", reconcileSchema, &reconciliation)
	totalTokens += tokens
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	candidateEmbs := make(map[string][]float32, len(candidates))
	for _, c := range candidates {
		candidateEmbs[c.text] = c.embedding
	}

	// Execute reconciliation actions
	result := &AddResult{}
	var deletedTexts []string
	for _, action := range reconciliation.Memory {
		switch action.Event {
		case "ADD":
			emb, ok := candidateEmbs[action.Text]
			if !ok {
				var err error
				emb, err = m.embedder.Embed(ctx, action.Text)
				if err != nil {
					return nil, fmt.Errorf("embed new fact: %w", err)
				}
			}
			id, err := m.store.InsertFact(userID, action.Text, hashFact(action.Text), emb)
			if err != nil {
				return nil, fmt.Errorf("insert fact: %w", err)
			}
			result.Added = append(result.Added, Fact{ID: id, Text: action.Text, UserID: userID})

		case "UPDATE":
			realID, ok := intToID[action.ID]
			if !ok {
				continue
			}
			if old, exists := existingByID[realID]; exists {
				deletedTexts = append(deletedTexts, old.Text)
			}
			emb, err := m.embedder.Embed(ctx, action.Text)
			if err != nil {
				return nil, fmt.Errorf("embed updated fact: %w", err)
			}
			if err := m.store.UpdateFact(realID, action.Text, hashFact(action.Text), emb); err != nil {
				return nil, fmt.Errorf("update fact: %w", err)
			}
			result.Updated = append(result.Updated, Fact{ID: realID, Text: action.Text, UserID: userID})

		case "DELETE":
			realID, ok := intToID[action.ID]
			if !ok {
				continue
			}
			if old, exists := existingByID[realID]; exists {
				deletedTexts = append(deletedTexts, old.Text)
			}
			if err := m.store.DeleteFact(realID); err != nil {
				return nil, fmt.Errorf("delete fact: %w", err)
			}
			result.Deleted = append(result.Deleted, realID)

		case "NOOP":
			result.Noops++
		}
	}

	// Cleanup stale relations after fact updates/deletes
	if len(deletedTexts) > 0 {
		if cleaned, err := m.store.CleanupStaleRelations(userID, deletedTexts); err != nil {
			log.Printf("stale relation cleanup error (non-fatal): %v", err)
		} else if cleaned > 0 {
			log.Printf("cleaned %d stale relations", cleaned)
		}
	}

	result.Tokens = totalTokens
	return result, nil
}

// --- Graph Pipeline ---

func (m *Memory) addGraph(ctx context.Context, messages []Message, userID string) (int, error) {
	messages = truncateMessages(messages, maxMessageChars)
	userContent := formatUserMessages(messages)

	var extraction EntityExtractionResult
	tokens, err := m.llm.Complete(ctx, entityExtractionPrompt, userContent, "entity_extraction", entityExtractionSchema, &extraction)
	if err != nil {
		return tokens, fmt.Errorf("extract entities: %w", err)
	}

	// Validate extraction — retry once on errors, log warnings
	issues := validateExtraction(&extraction)
	if len(issues.warnings) > 0 {
		log.Printf("entity extraction warnings: %s", strings.Join(issues.warnings, "; "))
	}
	if len(issues.errors) > 0 {
		log.Printf("entity extraction has %d errors, requesting correction: %s", len(issues.errors), strings.Join(issues.errors, "; "))

		previousJSON, _ := json.Marshal(extraction)
		correction := fmt.Sprintf(
			"Your previous extraction has %d issues:\n- %s\n\nPlease fix ALL issues and return the corrected extraction.",
			len(issues.errors), strings.Join(issues.errors, "\n- "),
		)

		var corrected EntityExtractionResult
		retryTokens, retryErr := m.llm.CompleteWithCorrection(
			ctx, entityExtractionPrompt, userContent, string(previousJSON), correction,
			"entity_extraction", entityExtractionSchema, &corrected,
		)
		tokens += retryTokens

		if retryErr == nil {
			newIssues := validateExtraction(&corrected)
			if len(newIssues.errors) < len(issues.errors) {
				extraction = corrected
			}
			if len(newIssues.errors) > 0 {
				log.Printf("correction still has %d errors (down from %d), proceeding with best result", len(newIssues.errors), len(issues.errors))
			}
		} else {
			log.Printf("correction request failed: %v, using original extraction", retryErr)
		}
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

		id, err := m.store.UpsertEntity(userID, name, e.Type)
		if err != nil {
			return tokens, fmt.Errorf("upsert entity %q: %w", name, err)
		}
		entityKeyToID[entityKey(name)] = id
		entNames = append(entNames, name)
		entIDs = append(entIDs, id)
	}

	// Batch embed all entity names
	if len(entNames) > 0 {
		entEmbs, err := m.embedder.EmbedBatch(ctx, entNames)
		if err != nil {
			return tokens, fmt.Errorf("embed entities: %w", err)
		}
		for i, id := range entIDs {
			if err := m.store.UpsertEntityEmbedding(id, entEmbs[i]); err != nil {
				return tokens, fmt.Errorf("store entity embedding %q: %w", entNames[i], err)
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
			id, err := m.store.UpsertEntity(userID, source, "other")
			if err != nil {
				return tokens, fmt.Errorf("upsert source entity %q: %w", source, err)
			}
			sourceID = id
			entityKeyToID[entityKey(source)] = id
		}

		targetID, ok := entityKeyToID[entityKey(target)]
		if !ok {
			id, err := m.store.UpsertEntity(userID, target, "other")
			if err != nil {
				return tokens, fmt.Errorf("upsert target entity %q: %w", target, err)
			}
			targetID = id
			entityKeyToID[entityKey(target)] = id
		}

		if err := m.store.UpsertRelation(userID, sourceID, r.Relation, targetID); err != nil {
			return tokens, fmt.Errorf("upsert relation: %w", err)
		}
	}

	return tokens, nil
}

// --- Read Path ---

// Search finds facts similar to the query for a given user.
// Results are re-ranked with temporal decay scoring and access counts are bumped.
func (m *Memory) Search(ctx context.Context, query string, userID string, limit int) ([]Fact, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 10
	}

	emb, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	facts, err := m.store.SearchFacts(emb, userID, limit*2)
	if err != nil {
		return nil, err
	}

	applyDecay(facts)
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
func (m *Memory) GetAll(ctx context.Context, userID string, limit int) ([]Fact, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	return m.store.GetAllFacts(userID, limit)
}

// Update manually updates a fact's text, re-embeds it.
func (m *Memory) Update(ctx context.Context, factID string, text string) error {
	emb, err := m.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed updated text: %w", err)
	}
	return m.store.UpdateFact(factID, text, hashFact(text), emb)
}

// Delete removes a single fact and its embedding.
func (m *Memory) Delete(ctx context.Context, factID string) error {
	return m.store.DeleteFact(factID)
}

// DeleteAll removes all data (facts, embeddings, entities, relations) for a user.
func (m *Memory) DeleteAll(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	return m.store.DeleteAllForUser(userID)
}

// SearchRelations finds relations matching a query for a user via entity embedding similarity.
func (m *Memory) SearchRelations(ctx context.Context, query string, userID string, limit int) ([]Relation, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	emb, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return m.store.SearchRelations(emb, userID, limit)
}

// GetAllRelations retrieves all relations for a user.
func (m *Memory) GetAllRelations(ctx context.Context, userID string, limit int) ([]Relation, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	return m.store.GetAllRelations(userID, limit)
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
	if opts != nil {
		if opts.MaxFacts > 0 {
			maxFacts = opts.MaxFacts
		}
		if opts.MaxRelations > 0 {
			maxRels = opts.MaxRelations
		}
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
		facts, factErr = m.store.SearchFacts(emb, userID, maxFacts*2)
	}()
	go func() {
		defer wg.Done()
		entityIDs, relErr = m.store.SearchEntityIDs(emb, userID, 10, 0.5)
	}()
	wg.Wait()

	if factErr != nil {
		return nil, fmt.Errorf("search facts: %w", factErr)
	}
	if relErr != nil {
		return nil, fmt.Errorf("search entity IDs: %w", relErr)
	}

	applyDecay(facts)
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
	rels, err := m.store.ExpandRelations(entityIDs, userID, 2, expandLimit)
	if err != nil {
		return nil, fmt.Errorf("expand relations: %w", err)
	}

	// Pre-format summary
	var sb strings.Builder

	if opts != nil && opts.IncludeProfile {
		profile, err := m.Profile(ctx, userID)
		if err != nil {
			log.Printf("profile generation error (non-fatal): %v", err)
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
func (m *Memory) Profile(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user_id is required")
	}

	facts, err := m.store.GetAllFacts(userID, 200)
	if err != nil {
		return "", fmt.Errorf("get facts: %w", err)
	}
	if len(facts) == 0 {
		return "", nil
	}

	currentHash := profileFactHash(facts)

	cached, storedHash, err := m.store.GetProfile(userID)
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	if cached != "" && storedHash == currentHash {
		return cached, nil
	}

	rels, err := m.store.GetAllRelations(userID, 100)
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

	_ = m.store.UpsertProfile(userID, result.Profile, currentHash)

	return result.Profile, nil
}

// --- Import / Export ---

// Export writes all facts, entities, and relations for a user as JSONL.
func (m *Memory) Export(ctx context.Context, userID string, w io.Writer) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	facts, err := m.store.GetAllFacts(userID, 100_000)
	if err != nil {
		return fmt.Errorf("export facts: %w", err)
	}
	for _, f := range facts {
		if err := enc.Encode(ExportRecord{Type: "fact", Text: f.Text}); err != nil {
			return err
		}
	}

	entities, err := m.store.GetAllEntities(userID, 100_000)
	if err != nil {
		return fmt.Errorf("export entities: %w", err)
	}
	for _, e := range entities {
		if err := enc.Encode(ExportRecord{Type: "entity", Name: e.Name, EntityType: e.Type}); err != nil {
			return err
		}
	}

	rels, err := m.store.GetAllRelations(userID, 100_000)
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
func (m *Memory) Import(ctx context.Context, userID string, r io.Reader) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}

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
			if _, err := m.store.InsertFact(userID, text, hashFact(text), embs[i]); err != nil {
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
			id, err := m.store.UpsertEntity(userID, rec.Name, rec.EntityType)
			if err != nil {
				return fmt.Errorf("upsert entity: %w", err)
			}
			if err := m.store.UpsertEntityEmbedding(id, embs[i]); err != nil {
				return fmt.Errorf("store entity embedding: %w", err)
			}
		}
	}

	for _, rec := range relationRecords {
		srcID, err := m.store.UpsertEntity(userID, rec.Source, "other")
		if err != nil {
			return fmt.Errorf("upsert source: %w", err)
		}
		tgtID, err := m.store.UpsertEntity(userID, rec.Target, "other")
		if err != nil {
			return fmt.Errorf("upsert target: %w", err)
		}
		if err := m.store.UpsertRelation(userID, srcID, rec.Relation, tgtID); err != nil {
			return fmt.Errorf("upsert relation: %w", err)
		}
	}

	return nil
}

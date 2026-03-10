package faktory

import (
	"context"
	"crypto/sha256"
	"fmt"
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
func (m *Memory) Add(ctx context.Context, messages []Message, userID string) (*AddResult, error) {
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
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

	factResult.Tokens += graphTokens
	return factResult, nil
}

// --- Fact Pipeline ---

func (m *Memory) addFacts(ctx context.Context, messages []Message, userID string) (*AddResult, error) {
	// Step 1: Extract facts from conversation
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

	// Step 2: For each fact, check hash, embed, and search similar
	type candidateFact struct {
		text      string
		hash      string
		embedding []float32
		similar   []Fact
	}

	var candidates []candidateFact
	for _, factText := range extraction.Facts {
		h := hashFact(factText)

		// Skip exact duplicates
		exists, err := m.store.FactExistsByHash(userID, h)
		if err != nil {
			return nil, fmt.Errorf("check hash: %w", err)
		}
		if exists {
			continue
		}

		emb, err := m.embedder.Embed(ctx, factText)
		if err != nil {
			return nil, fmt.Errorf("embed fact: %w", err)
		}

		similar, err := m.store.SearchFacts(emb, userID, 5)
		if err != nil {
			return nil, fmt.Errorf("search similar: %w", err)
		}

		candidates = append(candidates, candidateFact{
			text:      factText,
			hash:      h,
			embedding: emb,
			similar:   similar,
		})
	}

	if len(candidates) == 0 {
		return &AddResult{}, nil
	}

	// Step 3: Collect all similar existing facts, deduplicate (stable order)
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

	// Step 4: Map UUIDs to sequential integers (deterministic via existingOrder)
	idToInt := make(map[string]string)
	intToID := make(map[string]string)
	for idx, id := range existingOrder {
		intStr := fmt.Sprintf("%d", idx)
		idToInt[id] = intStr
		intToID[intStr] = id
	}

	// Build reconciliation prompt input
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

	// Step 5: Reconcile via LLM
	var reconciliation ReconcileResult
	tokens, err = m.llm.Complete(ctx, reconcilePrompt, reconcileInput, "reconcile_memory", reconcileSchema, &reconciliation)
	totalTokens += tokens
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	// Build a lookup from candidate text → embedding to avoid re-embedding ADDs
	candidateEmbs := make(map[string][]float32, len(candidates))
	for _, c := range candidates {
		candidateEmbs[c.text] = c.embedding
	}

	// Step 6: Execute actions
	result := &AddResult{}
	for _, action := range reconciliation.Memory {
		switch action.Event {
		case "ADD":
			// Reuse candidate embedding if text matches, otherwise re-embed
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
				continue // unknown ID, skip
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
			if err := m.store.DeleteFact(realID); err != nil {
				return nil, fmt.Errorf("delete fact: %w", err)
			}
			result.Deleted = append(result.Deleted, realID)

		case "NOOP":
			result.Noops++
		}
	}

	result.Tokens = totalTokens
	return result, nil
}

// --- Graph Pipeline ---

func (m *Memory) addGraph(ctx context.Context, messages []Message, userID string) (int, error) {
	messages = truncateMessages(messages, maxMessageChars)
	userContent := formatMessages(messages)

	var extraction EntityExtractionResult
	tokens, err := m.llm.Complete(ctx, entityExtractionPrompt, userContent, "entity_extraction", entityExtractionSchema, &extraction)
	if err != nil {
		return tokens, fmt.Errorf("extract entities: %w", err)
	}

	// Upsert entities and embed their names
	entityNameToID := make(map[string]string)
	for _, e := range extraction.Entities {
		id, err := m.store.UpsertEntity(userID, e.Name, e.Type)
		if err != nil {
			return tokens, fmt.Errorf("upsert entity %q: %w", e.Name, err)
		}
		entityNameToID[e.Name] = id

		emb, err := m.embedder.Embed(ctx, e.Name)
		if err != nil {
			return tokens, fmt.Errorf("embed entity %q: %w", e.Name, err)
		}
		if err := m.store.UpsertEntityEmbedding(id, emb); err != nil {
			return tokens, fmt.Errorf("store entity embedding %q: %w", e.Name, err)
		}
	}

	// Upsert relations
	for _, r := range extraction.Relations {
		sourceID, ok := entityNameToID[r.Source]
		if !ok {
			id, err := m.store.UpsertEntity(userID, r.Source, "other")
			if err != nil {
				return tokens, fmt.Errorf("upsert source entity %q: %w", r.Source, err)
			}
			sourceID = id
			entityNameToID[r.Source] = id
		}

		targetID, ok := entityNameToID[r.Target]
		if !ok {
			id, err := m.store.UpsertEntity(userID, r.Target, "other")
			if err != nil {
				return tokens, fmt.Errorf("upsert target entity %q: %w", r.Target, err)
			}
			targetID = id
			entityNameToID[r.Target] = id
		}

		if err := m.store.UpsertRelation(userID, sourceID, r.Relation, targetID); err != nil {
			return tokens, fmt.Errorf("upsert relation: %w", err)
		}
	}

	return tokens, nil
}

// --- Read Path ---

// Search finds facts similar to the query for a given user.
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

	return m.store.SearchFacts(emb, userID, limit)
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

// --- Helpers ---

// maxMessageChars is the approximate character budget for messages (~25K tokens).
const maxMessageChars = 100_000

// truncateMessages keeps the last N messages that fit within maxChars.
// Always keeps at least 1 message. Logs a warning if truncation occurs.
func truncateMessages(messages []Message, maxChars int) []Message {
	total := 0
	for _, m := range messages {
		total += len(m.Role) + len(m.Content) + 3 // "role: content\n"
	}
	if total <= maxChars {
		return messages
	}

	budget := maxChars
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		cost := len(messages[i].Role) + len(messages[i].Content) + 3
		if budget-cost < 0 && start < len(messages) {
			break
		}
		budget -= cost
		start = i
	}
	log.Printf("truncating conversation from %d to %d messages (%d chars exceeded %d limit)",
		len(messages), len(messages)-start, total, maxChars)
	return messages[start:]
}

func hashFact(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)
}

func formatMessages(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

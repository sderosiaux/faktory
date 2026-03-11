package faktory

import (
	"context"
	"fmt"
	"strings"
)

// candidateFact pairs an extracted fact with its hash, embedding, and similar existing facts.
type candidateFact struct {
	text       string
	hash       string
	embedding  []float32
	similar    []Fact
	importance int
}

// maxReconcileChunk is the maximum number of candidate facts sent in a single reconciliation LLM call.
const maxReconcileChunk = 10

// reconcileChunkResult holds the output of a single reconciliation chunk.
type reconcileChunkResult struct {
	added        []Fact
	updated      []Fact
	deleted      []string
	deletedTexts []string
	noops        int
	tokens       int
}

// reconcileChunk runs the reconciliation LLM call for a subset of candidate facts.
// Each chunk independently collects its similar existing facts, builds integer ID
// mappings, calls the LLM, and executes the resulting actions.
func (m *Memory) reconcileChunk(ctx context.Context, candidates []candidateFact, candidateEmbs map[string][]float32, userID, namespace string) (*reconcileChunkResult, error) {
	// Collect all similar existing facts for this chunk, deduplicate (stable order)
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

	// Context cap: keep only top 30 existing facts by highest similarity
	const maxReconcileContext = 30
	if len(existingByID) > maxReconcileContext {
		m.log.Warn("reconciliation context capped", "total", len(existingByID), "kept", maxReconcileContext)
		type scoredFact struct {
			id    string
			score float64
		}
		var scored []scoredFact
		bestScore := make(map[string]float64)
		for _, c := range candidates {
			for _, f := range c.similar {
				if f.Score > bestScore[f.ID] {
					bestScore[f.ID] = f.Score
				}
			}
		}
		for id, s := range bestScore {
			scored = append(scored, scoredFact{id: id, score: s})
		}
		for i := 0; i < len(scored); i++ {
			for j := i + 1; j < len(scored); j++ {
				if scored[j].score > scored[i].score {
					scored[i], scored[j] = scored[j], scored[i]
				}
			}
		}
		kept := make(map[string]bool)
		for i := 0; i < maxReconcileContext && i < len(scored); i++ {
			kept[scored[i].id] = true
		}
		newExistingByID := make(map[string]Fact)
		var newExistingOrder []string
		for _, id := range existingOrder {
			if kept[id] {
				newExistingByID[id] = existingByID[id]
				newExistingOrder = append(newExistingOrder, id)
			}
		}
		existingByID = newExistingByID
		existingOrder = newExistingOrder
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

	reconPrompt := reconcilePrompt
	if m.cfg.PromptReconciliation != "" {
		reconPrompt = m.cfg.PromptReconciliation
	}

	var reconciliation ReconcileResult
	tokens, err := m.llm.Complete(ctx, reconPrompt, reconcileInput, "reconcile_memory", reconcileSchema, &reconciliation)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	res := &reconcileChunkResult{tokens: tokens}

	for _, action := range reconciliation.Memory {
		switch action.Event {
		case "ADD":
			emb, ok := candidateEmbs[action.Text]
			if !ok {
				emb, err = m.embedder.Embed(ctx, action.Text)
				if err != nil {
					return nil, fmt.Errorf("embed new fact: %w", err)
				}
			}
			id, err := m.store.InsertFact(userID, namespace, action.Text, hashFact(action.Text), emb, 3)
			if err != nil {
				return nil, fmt.Errorf("insert fact: %w", err)
			}
			res.added = append(res.added, Fact{ID: id, Text: action.Text, UserID: userID})

		case "UPDATE":
			realID, ok := intToID[action.ID]
			if !ok {
				continue
			}
			if old, exists := existingByID[realID]; exists {
				res.deletedTexts = append(res.deletedTexts, old.Text)
			}
			emb, err := m.embedder.Embed(ctx, action.Text)
			if err != nil {
				return nil, fmt.Errorf("embed updated fact: %w", err)
			}
			if err := m.store.UpdateFact(realID, action.Text, hashFact(action.Text), emb); err != nil {
				return nil, fmt.Errorf("update fact: %w", err)
			}
			res.updated = append(res.updated, Fact{ID: realID, Text: action.Text, UserID: userID})

		case "DELETE":
			realID, ok := intToID[action.ID]
			if !ok {
				continue
			}
			if old, exists := existingByID[realID]; exists {
				res.deletedTexts = append(res.deletedTexts, old.Text)
			}
			if err := m.store.DeleteFact(realID); err != nil {
				return nil, fmt.Errorf("delete fact: %w", err)
			}
			res.deleted = append(res.deleted, realID)

		case "NOOP":
			res.noops++
		}
	}

	return res, nil
}

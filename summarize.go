package faktory

import (
	"context"
	"fmt"
)

// Summarize generates a session summary from messages and stores it as a special fact.
func (m *Memory) Summarize(ctx context.Context, messages []Message, userID string, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	if len(messages) == 0 {
		return nil
	}
	o := resolveOpts(opts)

	content := formatMessages(messages)
	var result SessionSummaryResult
	_, err := m.llm.Complete(ctx, sessionSummaryPrompt, content, "session_summary", sessionSummarySchema, &result)
	if err != nil {
		return fmt.Errorf("generate summary: %w", err)
	}
	if result.Summary == "" {
		return nil
	}

	emb, err := m.embedder.Embed(ctx, result.Summary)
	if err != nil {
		return fmt.Errorf("embed summary: %w", err)
	}
	_, err = m.store.InsertSummaryFact(userID, o.namespace, result.Summary, hashFact(result.Summary), emb)
	return err
}

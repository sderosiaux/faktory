package faktory

import (
	"encoding/json"
	"fmt"
)

// InsertSummaryFact inserts a fact with is_summary = 1.
func (s *Store) InsertSummaryFact(userID, namespace, text, hash string, embedding []float32) (string, error) {
	id := newID()
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO facts (id, user_id, namespace, text, hash, created_at, updated_at, is_summary) VALUES (?, ?, ?, ?, ?, ?, ?, 1)",
		id, userID, namespace, text, hash, ts, ts); err != nil {
		return "", fmt.Errorf("insert summary: %w", err)
	}

	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO fact_embeddings (id, embedding) VALUES (?, ?)", id, string(embJSON)); err != nil {
		return "", fmt.Errorf("insert embedding: %w", err)
	}

	return id, tx.Commit()
}

// GetSummaries returns summary facts for a user+namespace, newest first.
func (s *Store) GetSummaries(userID, namespace string, limit int) ([]Fact, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, text, hash, created_at, updated_at, access_count, source, confidence FROM facts WHERE user_id = ? AND namespace = ? AND is_summary = 1 ORDER BY created_at DESC LIMIT ?",
		userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &f.Source, &f.Confidence); err != nil {
			return nil, err
		}
		f.IsSummary = true
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

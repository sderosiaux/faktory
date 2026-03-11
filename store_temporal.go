package faktory

import "time"

// GetFactsAt returns facts that were valid at the given point in time.
// A fact is visible at time T if: valid_from <= T AND (invalid_at IS NULL OR invalid_at > T).
func (s *Store) GetFactsAt(userID, namespace string, at time.Time, limit int) ([]Fact, error) {
	atStr := at.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(`
		SELECT id, user_id, text, hash, created_at, updated_at, access_count, importance,
		       valid_from, COALESCE(invalid_at, ''), source, confidence
		FROM facts
		WHERE user_id = ? AND namespace = ?
		  AND valid_from != '' AND valid_from <= ?
		  AND (invalid_at IS NULL OR invalid_at > ?)
		ORDER BY created_at DESC
		LIMIT ?
	`, userID, namespace, atStr, atStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &f.Importance, &f.ValidFrom, &f.InvalidAt, &f.Source, &f.Confidence); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

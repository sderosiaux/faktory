package faktory

import (
	"database/sql"
	"sort"
	"strings"
)

const fts5Schema = `
CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
    id UNINDEXED,
    user_id UNINDEXED,
    namespace UNINDEXED,
    text,
    content='facts',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, id, user_id, namespace, text)
    VALUES (new.rowid, new.id, new.user_id, new.namespace, new.text);
END;

CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, id, user_id, namespace, text)
    VALUES('delete', old.rowid, old.id, old.user_id, old.namespace, old.text);
    INSERT INTO facts_fts(rowid, id, user_id, namespace, text)
    VALUES (new.rowid, new.id, new.user_id, new.namespace, new.text);
END;

CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, id, user_id, namespace, text)
    VALUES('delete', old.rowid, old.id, old.user_id, old.namespace, old.text);
END;
`

// migrateFTS5 creates the FTS5 virtual table and sync triggers.
// Returns nil (no-op) if FTS5 is not compiled into SQLite.
func migrateFTS5(db *sql.DB) error {
	_, err := db.Exec(fts5Schema)
	if err != nil && strings.Contains(err.Error(), "no such module") {
		return nil // FTS5 not available; BM25 search will gracefully return empty
	}
	return err
}

// sanitizeFTS5Query strips FTS5 operators and wraps each word in quotes
// to prevent syntax errors from user input containing special chars.
func sanitizeFTS5Query(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var sb strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	words := strings.Fields(sb.String())
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " ")
}

// SearchFactsBM25 searches facts using FTS5 full-text search with BM25 ranking.
// Returns nil (no results) gracefully if FTS5 is not available.
func (s *Store) SearchFactsBM25(query, userID, namespace string, limit int) ([]Fact, error) {
	query = sanitizeFTS5Query(query)
	if query == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT f.id, f.user_id, f.text, f.hash, f.created_at, f.updated_at, f.access_count,
		       bm25(facts_fts) AS rank
		FROM facts_fts fts
		JOIN facts f ON f.rowid = fts.rowid
		WHERE facts_fts MATCH ?
		  AND fts.user_id = ?
		  AND fts.namespace = ?
		ORDER BY rank
		LIMIT ?
	`, query, userID, namespace, limit)
	if err != nil {
		return nil, nil //nolint:nilerr // graceful: FTS5 table may not exist
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var rank float64
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &rank); err != nil {
			return nil, err
		}
		// bm25() returns negative scores (more negative = more relevant); normalize to (0,1]
		if rank < 0 {
			rank = -rank
		}
		f.Score = 1.0 / (1.0 + rank)
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// fuseScores merges vector and BM25 results using weighted combination.
// Facts appearing in both lists get a blended score; facts in only one list
// get a partial score scaled by the respective weight.
func fuseScores(vectorFacts, bm25Facts []Fact, bm25Weight float64) []Fact {
	bm25Scores := make(map[string]float64, len(bm25Facts))
	for _, f := range bm25Facts {
		bm25Scores[f.ID] = f.Score
	}

	seen := make(map[string]bool)
	var merged []Fact

	for _, f := range vectorFacts {
		seen[f.ID] = true
		bm25Score := bm25Scores[f.ID] // 0 if not in BM25 results
		f.Score = (1-bm25Weight)*f.Score + bm25Weight*bm25Score
		merged = append(merged, f)
	}

	for _, f := range bm25Facts {
		if !seen[f.ID] {
			f.Score = bm25Weight * f.Score
			merged = append(merged, f)
		}
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	return merged
}

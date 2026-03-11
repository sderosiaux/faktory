package faktory

import (
	"encoding/json"
	"fmt"
	"strings"
)

// --- Entities ---

func (s *Store) GetAllEntities(userID, namespace string, limit int) ([]Entity, error) {
	rows, err := s.db.Query("SELECT id, name, type FROM entities WHERE user_id = ? AND namespace = ? ORDER BY created_at DESC LIMIT ?", userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entities []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

func (s *Store) UpsertEntity(userID, namespace, name, entityType string) (string, error) {
	id := newID()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO entities (id, user_id, namespace, name, type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, namespace, name, type) DO UPDATE SET updated_at = ?`,
		id, userID, namespace, name, entityType, ts, ts, ts)
	if err != nil {
		return "", fmt.Errorf("upsert entity: %w", err)
	}

	// Return the actual ID (might be existing)
	var actualID string
	err = s.db.QueryRow("SELECT id FROM entities WHERE user_id = ? AND namespace = ? AND name = ? AND type = ?", userID, namespace, name, entityType).Scan(&actualID)
	return actualID, err
}

func (s *Store) UpsertEntityEmbedding(entityID string, embedding []float32) error {
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM entity_embeddings WHERE id = ?", entityID); err != nil {
		return fmt.Errorf("delete old entity embedding: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO entity_embeddings (id, embedding) VALUES (?, ?)", entityID, string(embJSON)); err != nil {
		return fmt.Errorf("insert entity embedding: %w", err)
	}
	return tx.Commit()
}

// --- Relations ---

func (s *Store) UpsertRelation(userID, namespace, sourceID, relation, targetID string) error {
	id := newID()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO relations (id, user_id, namespace, source_id, relation, target_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, namespace, source_id, relation, target_id) DO UPDATE SET updated_at = ?`,
		id, userID, namespace, sourceID, relation, targetID, ts, ts, ts)
	return err
}

func (s *Store) GetAllRelations(userID, namespace string, limit int) ([]Relation, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.relation,
		       s.name, s.type,
		       t.name, t.type
		FROM relations r
		JOIN entities s ON s.id = r.source_id
		JOIN entities t ON t.id = r.target_id
		WHERE r.user_id = ? AND r.namespace = ?
		ORDER BY r.created_at DESC
		LIMIT ?
	`, userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rels []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.ID, &r.Relation, &r.Source, &r.SourceType, &r.Target, &r.TargetType); err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

// ExpandRelations performs BFS from seed entity IDs, following relation edges
// up to maxDepth hops. Returns deduplicated relations with hop distance.
func (s *Store) ExpandRelations(seedIDs []string, userID, namespace string, maxDepth int, limit int) ([]Relation, error) {
	if len(seedIDs) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)     // relation IDs already collected
	frontier := make(map[string]bool) // entity IDs to explore
	for _, id := range seedIDs {
		frontier[id] = true
	}

	var result []Relation

	for depth := 0; depth < maxDepth && len(frontier) > 0 && len(result) < limit; depth++ {
		ids := make([]string, 0, len(frontier))
		for id := range frontier {
			ids = append(ids, id)
		}

		placeholders := make([]string, len(ids))
		args := make([]any, 0, len(ids)*2+3)
		args = append(args, userID, namespace)
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		inClause := strings.Join(placeholders, ",")
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, limit-len(result))

		q := fmt.Sprintf(`
			SELECT r.id, r.relation,
			       s.id, s.name, s.type,
			       t.id, t.name, t.type
			FROM relations r
			JOIN entities s ON s.id = r.source_id
			JOIN entities t ON t.id = r.target_id
			WHERE r.user_id = ? AND r.namespace = ?
			  AND (r.source_id IN (%s) OR r.target_id IN (%s))
			LIMIT ?
		`, inClause, inClause)

		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}

		nextFrontier := make(map[string]bool)
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var r Relation
				var srcID, tgtID string
				if err := rows.Scan(&r.ID, &r.Relation, &srcID, &r.Source, &r.SourceType, &tgtID, &r.Target, &r.TargetType); err != nil {
					return err
				}
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				result = append(result, r)
				if !frontier[srcID] {
					nextFrontier[srcID] = true
				}
				if !frontier[tgtID] {
					nextFrontier[tgtID] = true
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}

		frontier = nextFrontier
	}

	return result, nil
}

// SearchEntityIDs returns entity IDs matching the query embedding via KNN.
// Only entities with cosine similarity >= minSimilarity are returned.
func (s *Store) SearchEntityIDs(queryEmbedding []float32, userID, namespace string, limit int, minSimilarity float64) ([]string, error) {
	embJSON, err := json.Marshal(queryEmbedding)
	if err != nil {
		return nil, err
	}
	kFetch := limit * 20
	if kFetch > 200 {
		kFetch = 200
	}

	rows, err := s.db.Query(`
		SELECT e.id, ee.distance
		FROM entity_embeddings ee
		JOIN entities e ON e.id = ee.id
		WHERE ee.embedding MATCH ?
		  AND k = ?
		  AND e.user_id = ?
		  AND e.namespace = ?
	`, string(embJSON), kFetch, userID, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		var dist float64
		if err := rows.Scan(&id, &dist); err != nil {
			return nil, err
		}
		if 1-dist < minSimilarity {
			continue
		}
		ids = append(ids, id)
		if len(ids) >= limit {
			break
		}
	}
	return ids, rows.Err()
}

func (s *Store) SearchRelations(queryEmbedding []float32, userID, namespace string, limit int) ([]Relation, error) {
	embJSON, err := json.Marshal(queryEmbedding)
	if err != nil {
		return nil, err
	}

	// KNN search entity_embeddings, over-fetch then filter by user_id via JOIN
	kFetch := limit * 20
	if kFetch > 200 {
		kFetch = 200
	}

	// Find entity IDs matching the query embedding
	entityRows, err := s.db.Query(`
		SELECT e.id
		FROM entity_embeddings ee
		JOIN entities e ON e.id = ee.id
		WHERE ee.embedding MATCH ?
		  AND k = ?
		  AND e.user_id = ?
		  AND e.namespace = ?
	`, string(embJSON), kFetch, userID, namespace)
	if err != nil {
		return nil, err
	}
	defer entityRows.Close()
	var entityIDs []string
	for entityRows.Next() {
		var id string
		if err := entityRows.Scan(&id); err != nil {
			return nil, err
		}
		entityIDs = append(entityIDs, id)
	}
	if err := entityRows.Err(); err != nil {
		return nil, err
	}

	if len(entityIDs) == 0 {
		return []Relation{}, nil
	}

	// Build IN clause for matched entity IDs
	placeholders := make([]string, len(entityIDs))
	args := make([]any, 0, len(entityIDs)*2+3)
	args = append(args, userID, namespace)
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")

	// Duplicate the entity ID args for the second IN clause (target_id)
	for _, id := range entityIDs {
		args = append(args, id)
	}
	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT r.id, r.relation,
		       s.name, s.type,
		       t.name, t.type
		FROM relations r
		JOIN entities s ON s.id = r.source_id
		JOIN entities t ON t.id = r.target_id
		WHERE r.user_id = ? AND r.namespace = ?
		  AND (r.source_id IN (%s) OR r.target_id IN (%s))
		ORDER BY r.created_at DESC
		LIMIT ?
	`, inClause, inClause)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rels []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.ID, &r.Relation, &r.Source, &r.SourceType, &r.Target, &r.TargetType); err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

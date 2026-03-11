package faktory

import (
	"fmt"
	"math"
	"strings"
)

// AssignCluster assigns a cluster_id to an entity based on embedding similarity.
// Uses single-linkage clustering: if the new entity is similar (cosine > threshold)
// to any existing entity, it joins that cluster. If multiple matches span different
// clusters, they are merged into the lowest cluster_id.
func (s *Store) AssignCluster(entityID, userID, namespace string, embedding []float32, threshold float64) error {
	similarIDs, err := s.SearchEntityIDs(embedding, userID, namespace, 50, threshold)
	if err != nil {
		return fmt.Errorf("search similar entities: %w", err)
	}
	var filtered []string
	for _, id := range similarIDs {
		if id != entityID {
			filtered = append(filtered, id)
		}
	}
	similarIDs = filtered

	if len(similarIDs) == 0 {
		var maxCluster int
		err := s.db.QueryRow(
			"SELECT COALESCE(MAX(cluster_id), 0) FROM entities WHERE user_id = ? AND namespace = ?",
			userID, namespace,
		).Scan(&maxCluster)
		if err != nil {
			return fmt.Errorf("get max cluster: %w", err)
		}
		_, err = s.db.Exec("UPDATE entities SET cluster_id = ? WHERE id = ?", maxCluster+1, entityID)
		return err
	}

	placeholders := make([]string, len(similarIDs))
	args := make([]any, 0, len(similarIDs)+2)
	args = append(args, userID, namespace)
	for i, id := range similarIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")
	rows, err := s.db.Query(
		fmt.Sprintf("SELECT DISTINCT cluster_id FROM entities WHERE user_id = ? AND namespace = ? AND id IN (%s) AND cluster_id > 0", inClause),
		args...,
	)
	if err != nil {
		return fmt.Errorf("get cluster ids: %w", err)
	}
	defer rows.Close()

	var clusterIDs []int
	for rows.Next() {
		var cid int
		if err := rows.Scan(&cid); err != nil {
			return err
		}
		clusterIDs = append(clusterIDs, cid)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(clusterIDs) == 0 {
		var maxCluster int
		err := s.db.QueryRow(
			"SELECT COALESCE(MAX(cluster_id), 0) FROM entities WHERE user_id = ? AND namespace = ?",
			userID, namespace,
		).Scan(&maxCluster)
		if err != nil {
			return fmt.Errorf("get max cluster: %w", err)
		}
		newCluster := maxCluster + 1
		if _, err := s.db.Exec("UPDATE entities SET cluster_id = ? WHERE id = ?", newCluster, entityID); err != nil {
			return err
		}
		for _, id := range similarIDs {
			if _, err := s.db.Exec("UPDATE entities SET cluster_id = ? WHERE id = ?", newCluster, id); err != nil {
				return err
			}
		}
		return nil
	}

	minCluster := clusterIDs[0]
	for _, cid := range clusterIDs[1:] {
		if cid < minCluster {
			minCluster = cid
		}
	}
	if _, err := s.db.Exec("UPDATE entities SET cluster_id = ? WHERE id = ?", minCluster, entityID); err != nil {
		return err
	}
	if len(clusterIDs) > 1 {
		for _, cid := range clusterIDs {
			if cid == minCluster {
				continue
			}
			if _, err := s.db.Exec(
				"UPDATE entities SET cluster_id = ? WHERE user_id = ? AND namespace = ? AND cluster_id = ?",
				minCluster, userID, namespace, cid,
			); err != nil {
				return fmt.Errorf("merge cluster %d into %d: %w", cid, minCluster, err)
			}
		}
	}
	for _, id := range similarIDs {
		if _, err := s.db.Exec(
			"UPDATE entities SET cluster_id = ? WHERE id = ? AND cluster_id = 0",
			minCluster, id,
		); err != nil {
			return err
		}
	}
	return nil
}

// GetClusterEntityIDs returns all entity IDs in the same clusters as the given entities.
// Entities with cluster_id 0 (unassigned) are not expanded -- only the queried ID is returned.
func (s *Store) GetClusterEntityIDs(entityIDs []string, userID, namespace string) ([]string, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(entityIDs))
	args := make([]any, 0, len(entityIDs)+2)
	args = append(args, userID, namespace)
	for i, id := range entityIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")
	rows, err := s.db.Query(
		fmt.Sprintf("SELECT id, cluster_id FROM entities WHERE user_id = ? AND namespace = ? AND id IN (%s)", inClause),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var clusterIDs []int
	clusterSet := make(map[int]bool)
	for rows.Next() {
		var id string
		var cid int
		if err := rows.Scan(&id, &cid); err != nil {
			return nil, err
		}
		seen[id] = true
		if cid > 0 && !clusterSet[cid] {
			clusterIDs = append(clusterIDs, cid)
			clusterSet[cid] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(clusterIDs) == 0 {
		result := make([]string, 0, len(entityIDs))
		for _, id := range entityIDs {
			if seen[id] {
				result = append(result, id)
			}
		}
		return result, nil
	}

	cPlaceholders := make([]string, len(clusterIDs))
	cArgs := make([]any, 0, len(clusterIDs)+2)
	cArgs = append(cArgs, userID, namespace)
	for i, cid := range clusterIDs {
		cPlaceholders[i] = "?"
		cArgs = append(cArgs, cid)
	}
	cInClause := strings.Join(cPlaceholders, ",")
	expandRows, err := s.db.Query(
		fmt.Sprintf("SELECT id FROM entities WHERE user_id = ? AND namespace = ? AND cluster_id IN (%s)", cInClause),
		cArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer expandRows.Close()

	for expandRows.Next() {
		var id string
		if err := expandRows.Scan(&id); err != nil {
			return nil, err
		}
		seen[id] = true
	}
	if err := expandRows.Err(); err != nil {
		return nil, err
	}

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result, nil
}

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

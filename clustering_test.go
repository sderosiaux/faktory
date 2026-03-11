package faktory

import "testing"

func TestClustering_RelatedEntitiesGrouped(t *testing.T) {
	s := tempStore(t, 4)
	idA, err := s.UpsertEntity("alice", "", "Spotify", "product")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := s.UpsertEntity("alice", "", "Music Streaming", "concept")
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertEntityEmbedding(idA, []float32{1, 0, 0, 0})
	s.UpsertEntityEmbedding(idB, []float32{0.9, 0.3, 0, 0})
	if err := s.AssignCluster(idA, "alice", "", []float32{1, 0, 0, 0}, 0.6); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignCluster(idB, "alice", "", []float32{0.9, 0.3, 0, 0}, 0.6); err != nil {
		t.Fatal(err)
	}
	if a, b := getClusterID(t, s, idA), getClusterID(t, s, idB); a != b {
		t.Errorf("expected same cluster, got %d and %d", a, b)
	}
}

func TestClustering_UnrelatedEntitiesSeparate(t *testing.T) {
	s := tempStore(t, 4)
	idA, _ := s.UpsertEntity("alice", "", "Spotify", "product")
	idC, _ := s.UpsertEntity("alice", "", "Cooking", "concept")
	s.UpsertEntityEmbedding(idA, []float32{1, 0, 0, 0})
	s.UpsertEntityEmbedding(idC, []float32{0, 0, 1, 0})
	s.AssignCluster(idA, "alice", "", []float32{1, 0, 0, 0}, 0.6)
	s.AssignCluster(idC, "alice", "", []float32{0, 0, 1, 0}, 0.6)
	if a, c := getClusterID(t, s, idA), getClusterID(t, s, idC); a == c {
		t.Errorf("expected different clusters, both got %d", a)
	}
}

func TestClustering_MergesOnBridge(t *testing.T) {
	s := tempStore(t, 4)
	idA, _ := s.UpsertEntity("alice", "", "Spotify", "product")
	idB, _ := s.UpsertEntity("alice", "", "Music Streaming", "concept")
	idC, _ := s.UpsertEntity("alice", "", "Audio Tech", "concept")
	s.UpsertEntityEmbedding(idA, []float32{1, 0, 0, 0})
	s.UpsertEntityEmbedding(idB, []float32{0.9, 0.3, 0, 0})
	s.UpsertEntityEmbedding(idC, []float32{0, 0, 1, 0})
	s.AssignCluster(idA, "alice", "", []float32{1, 0, 0, 0}, 0.6)
	s.AssignCluster(idB, "alice", "", []float32{0.9, 0.3, 0, 0}, 0.6)
	s.AssignCluster(idC, "alice", "", []float32{0, 0, 1, 0}, 0.6)
	if getClusterID(t, s, idA) == getClusterID(t, s, idC) {
		t.Fatal("precondition: A/B and C should be in different clusters before bridge")
	}
	idD, _ := s.UpsertEntity("alice", "", "Sound Engineering", "concept")
	s.UpsertEntityEmbedding(idD, []float32{0.7, 0.2, 0.7, 0})
	s.AssignCluster(idD, "alice", "", []float32{0.7, 0.2, 0.7, 0}, 0.6)
	cA, cB, cC, cD := getClusterID(t, s, idA), getClusterID(t, s, idB), getClusterID(t, s, idC), getClusterID(t, s, idD)
	if cA != cB || cB != cC || cC != cD {
		t.Errorf("expected all same cluster after bridge, got A=%d B=%d C=%d D=%d", cA, cB, cC, cD)
	}
}

func TestClustering_GetClusterEntityIDs(t *testing.T) {
	s := tempStore(t, 4)
	idA, _ := s.UpsertEntity("alice", "", "EntityA", "concept")
	idB, _ := s.UpsertEntity("alice", "", "EntityB", "concept")
	idC, _ := s.UpsertEntity("alice", "", "EntityC", "concept")
	setClusterID(t, s, idA, 1)
	setClusterID(t, s, idB, 1)
	setClusterID(t, s, idC, 2)
	ids, err := s.GetClusterEntityIDs([]string{idA}, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(ids), ids)
	}
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	if !m[idA] || !m[idB] {
		t.Errorf("expected A and B, got %v", ids)
	}
	ids, _ = s.GetClusterEntityIDs([]string{idC}, "alice", "")
	if len(ids) != 1 || ids[0] != idC {
		t.Errorf("expected only C, got %v", ids)
	}
	ids, _ = s.GetClusterEntityIDs([]string{idA, idC}, "alice", "")
	if len(ids) != 3 {
		t.Errorf("expected 3, got %d: %v", len(ids), ids)
	}
}

func TestClustering_SkipsZeroCluster(t *testing.T) {
	s := tempStore(t, 4)
	idA, _ := s.UpsertEntity("alice", "", "Unclustered1", "concept")
	idB, _ := s.UpsertEntity("alice", "", "Unclustered2", "concept")
	ids, _ := s.GetClusterEntityIDs([]string{idA}, "alice", "")
	if len(ids) != 1 || ids[0] != idA {
		t.Errorf("cluster 0 should not expand, got %v (idB=%s)", ids, idB)
	}
}

func TestClustering_UserIsolation(t *testing.T) {
	s := tempStore(t, 4)
	idAlice, _ := s.UpsertEntity("alice", "", "SharedName", "concept")
	idBob, _ := s.UpsertEntity("bob", "", "SharedName", "concept")
	setClusterID(t, s, idAlice, 1)
	setClusterID(t, s, idBob, 1)
	ids, _ := s.GetClusterEntityIDs([]string{idAlice}, "alice", "")
	if len(ids) != 1 || ids[0] != idAlice {
		t.Errorf("should not leak across users, got %v (bob=%s)", ids, idBob)
	}
}

func TestCosineSimilarity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		a, b      []float32
		want, tol float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0, 0.001},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0, 0.001},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0, 0.001},
		{"similar", []float32{1, 0, 0, 0}, []float32{0.9, 0.3, 0, 0}, 0.948, 0.01},
		{"zero_a", []float32{0, 0, 0}, []float32{1, 0, 0}, 0.0, 0.001},
		{"zero_b", []float32{1, 0, 0}, []float32{0, 0, 0}, 0.0, 0.001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if got < tc.want-tc.tol || got > tc.want+tc.tol {
				t.Errorf("cosineSimilarity = %f, want %f +/- %f", got, tc.want, tc.tol)
			}
		})
	}
}

func getClusterID(t *testing.T, s *Store, entityID string) int {
	t.Helper()
	var cid int
	if err := s.db.QueryRow("SELECT cluster_id FROM entities WHERE id = ?", entityID).Scan(&cid); err != nil {
		t.Fatalf("get cluster_id for %s: %v", entityID, err)
	}
	return cid
}

func setClusterID(t *testing.T, s *Store, entityID string, clusterID int) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE entities SET cluster_id = ? WHERE id = ?", clusterID, entityID); err != nil {
		t.Fatalf("set cluster_id for %s: %v", entityID, err)
	}
}

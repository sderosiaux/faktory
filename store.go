package faktory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	sqlite_vec.Auto()
}

const schema = `
CREATE TABLE IF NOT EXISTS facts (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    text       TEXT NOT NULL,
    hash       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS entities (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(user_id, name, type)
);

CREATE TABLE IF NOT EXISTS relations (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    source_id  TEXT NOT NULL REFERENCES entities(id),
    relation   TEXT NOT NULL,
    target_id  TEXT NOT NULL REFERENCES entities(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(user_id, source_id, relation, target_id)
);

CREATE INDEX IF NOT EXISTS idx_facts_user ON facts(user_id);
CREATE INDEX IF NOT EXISTS idx_facts_hash ON facts(user_id, hash);
CREATE INDEX IF NOT EXISTS idx_entities_user ON entities(user_id);
CREATE INDEX IF NOT EXISTS idx_relations_user ON relations(user_id);
CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);
`

type Store struct {
	db        *sql.DB
	dimension int
}

func OpenStore(dbPath string, dimension int) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Verify sqlite-vec is loaded
	var vecVersion string
	if err := db.QueryRow("SELECT vec_version()").Scan(&vecVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite-vec extension not available: %w", err)
	}

	// Run schema migrations
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	// Create vec0 virtual table (cannot use IF NOT EXISTS with virtual tables in all versions)
	vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS fact_embeddings USING vec0(id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`, dimension)
	if _, err := db.Exec(vecSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("create fact_embeddings: %w", err)
	}

	return &Store{db: db, dimension: dimension}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newID() string {
	return uuid.New().String()
}

// --- Facts ---

func (s *Store) InsertFact(userID, text, hash string, embedding []float32) (string, error) {
	id := newID()
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO facts (id, user_id, text, hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, userID, text, hash, ts, ts); err != nil {
		return "", fmt.Errorf("insert fact: %w", err)
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

func (s *Store) UpdateFact(id, text, hash string, embedding []float32) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE facts SET text = ?, hash = ?, updated_at = ? WHERE id = ?", text, hash, now(), id); err != nil {
		return fmt.Errorf("update fact: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete old embedding: %w", err)
	}

	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO fact_embeddings (id, embedding) VALUES (?, ?)", id, string(embJSON)); err != nil {
		return fmt.Errorf("insert new embedding: %w", err)
	}

	return tx.Commit()
}

func (s *Store) DeleteFact(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete embedding: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM facts WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}

	return tx.Commit()
}

func (s *Store) GetFact(id string) (*Fact, error) {
	var f Fact
	err := s.db.QueryRow("SELECT id, user_id, text, hash, created_at, updated_at FROM facts WHERE id = ?", id).
		Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Store) GetAllFacts(userID string, limit int) ([]Fact, error) {
	rows, err := s.db.Query("SELECT id, user_id, text, hash, created_at, updated_at FROM facts WHERE user_id = ? ORDER BY created_at DESC LIMIT ?", userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func (s *Store) SearchFacts(queryEmbedding []float32, userID string, limit int) ([]Fact, error) {
	embJSON, err := json.Marshal(queryEmbedding)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT f.id, f.user_id, f.text, f.hash, f.created_at, f.updated_at, e.distance
		FROM fact_embeddings e
		JOIN facts f ON f.id = e.id
		WHERE e.embedding MATCH ?
		  AND k = ?
		  AND f.user_id = ?
		ORDER BY e.distance
	`, string(embJSON), limit, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var dist float64
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &dist); err != nil {
			return nil, err
		}
		f.Score = 1 - dist // cosine distance → similarity
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func (s *Store) FactExistsByHash(userID, hash string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE user_id = ? AND hash = ?", userID, hash).Scan(&count)
	return count > 0, err
}

func (s *Store) DeleteAllForUser(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Get all fact IDs for this user to delete embeddings
	rows, err := tx.Query("SELECT id FROM facts WHERE user_id = ?", userID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM facts WHERE user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM relations WHERE user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM entities WHERE user_id = ?", userID); err != nil {
		return err
	}

	return tx.Commit()
}

// --- Entities ---

func (s *Store) UpsertEntity(userID, name, entityType string) (string, error) {
	id := newID()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO entities (id, user_id, name, type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, name, type) DO UPDATE SET updated_at = ?`,
		id, userID, name, entityType, ts, ts, ts)
	if err != nil {
		return "", fmt.Errorf("upsert entity: %w", err)
	}

	// Return the actual ID (might be existing)
	var actualID string
	err = s.db.QueryRow("SELECT id FROM entities WHERE user_id = ? AND name = ? AND type = ?", userID, name, entityType).Scan(&actualID)
	return actualID, err
}

// --- Relations ---

func (s *Store) UpsertRelation(userID, sourceID, relation, targetID string) error {
	id := newID()
	ts := now()
	_, err := s.db.Exec(`INSERT INTO relations (id, user_id, source_id, relation, target_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, source_id, relation, target_id) DO UPDATE SET updated_at = ?`,
		id, userID, sourceID, relation, targetID, ts, ts, ts)
	return err
}

func (s *Store) GetAllRelations(userID string, limit int) ([]Relation, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.relation,
		       s.name, s.type,
		       t.name, t.type
		FROM relations r
		JOIN entities s ON s.id = r.source_id
		JOIN entities t ON t.id = r.target_id
		WHERE r.user_id = ?
		ORDER BY r.created_at DESC
		LIMIT ?
	`, userID, limit)
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

func (s *Store) SearchRelations(query string, userID string, limit int) ([]Relation, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT r.id, r.relation,
		       s.name, s.type,
		       t.name, t.type
		FROM relations r
		JOIN entities s ON s.id = r.source_id
		JOIN entities t ON t.id = r.target_id
		WHERE r.user_id = ?
		  AND (s.name LIKE ? OR t.name LIKE ? OR r.relation LIKE ?)
		ORDER BY r.created_at DESC
		LIMIT ?
	`, userID, pattern, pattern, pattern, limit)
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

package faktory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

CREATE TABLE IF NOT EXISTS processed_conversations (
    user_id      TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY(user_id, content_hash)
);
`

type Store struct {
	db        *sql.DB
	dimension int
}

func OpenStore(dbPath string, dimension int) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=-20000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single connection avoids WAL contention for this single-process use case
	db.SetMaxOpenConns(1)

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

	// Create vec0 virtual tables
	for _, tbl := range []string{"fact_embeddings", "entity_embeddings"} {
		vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`, tbl, dimension)
		if _, err := db.Exec(vecSQL); err != nil {
			db.Close()
			return nil, fmt.Errorf("create %s: %w", tbl, err)
		}
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

	// Over-fetch from vec0 because KNN runs globally (no user_id filter inside
	// the virtual table). The JOIN + WHERE filters to the target user afterward.
	kFetch := limit * 20
	if kFetch > 200 {
		kFetch = 200
	}

	rows, err := s.db.Query(`
		SELECT f.id, f.user_id, f.text, f.hash, f.created_at, f.updated_at, e.distance
		FROM fact_embeddings e
		JOIN facts f ON f.id = e.id
		WHERE e.embedding MATCH ?
		  AND k = ?
		  AND f.user_id = ?
		ORDER BY e.distance
		LIMIT ?
	`, string(embJSON), kFetch, userID, limit)
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

func queryIDs(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) DeleteAllForUser(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete fact embeddings
	factIDs, err := queryIDs(tx, "SELECT id FROM facts WHERE user_id = ?", userID)
	if err != nil {
		return err
	}
	for _, id := range factIDs {
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

	// Delete entity embeddings
	entIDs, err := queryIDs(tx, "SELECT id FROM entities WHERE user_id = ?", userID)
	if err != nil {
		return err
	}
	for _, id := range entIDs {
		if _, err := tx.Exec("DELETE FROM entity_embeddings WHERE id = ?", id); err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM entities WHERE user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM processed_conversations WHERE user_id = ?", userID); err != nil {
		return err
	}

	return tx.Commit()
}

// --- Conversation Dedup ---

func (s *Store) ConversationExists(userID, contentHash string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM processed_conversations WHERE user_id = ? AND content_hash = ?", userID, contentHash).Scan(&count)
	return count > 0, err
}

func (s *Store) MarkConversationProcessed(userID, contentHash string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO processed_conversations (user_id, content_hash, created_at) VALUES (?, ?, ?)", userID, contentHash, now())
	return err
}

// --- Entities ---

func (s *Store) GetAllEntities(userID string, limit int) ([]Entity, error) {
	rows, err := s.db.Query("SELECT id, name, type FROM entities WHERE user_id = ? ORDER BY created_at DESC LIMIT ?", userID, limit)
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

func (s *Store) UpsertEntityEmbedding(entityID string, embedding []float32) error {
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	// Delete old embedding if exists, then insert new
	if _, err := s.db.Exec("DELETE FROM entity_embeddings WHERE id = ?", entityID); err != nil {
		return fmt.Errorf("delete old entity embedding: %w", err)
	}
	if _, err := s.db.Exec("INSERT INTO entity_embeddings (id, embedding) VALUES (?, ?)", entityID, string(embJSON)); err != nil {
		return fmt.Errorf("insert entity embedding: %w", err)
	}
	return nil
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

func (s *Store) SearchRelations(queryEmbedding []float32, userID string, limit int) ([]Relation, error) {
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
	`, string(embJSON), kFetch, userID)
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
	args := make([]any, 0, len(entityIDs)*2+2)
	args = append(args, userID)
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
		WHERE r.user_id = ?
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

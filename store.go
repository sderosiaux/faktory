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
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    namespace       TEXT NOT NULL DEFAULT '',
    text            TEXT NOT NULL,
    hash            TEXT NOT NULL,
    importance      INTEGER NOT NULL DEFAULT 3,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    access_count    INTEGER NOT NULL DEFAULT 0,
    last_accessed_at TEXT,
    is_summary      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS entities (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    cluster_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(user_id, namespace, name, type)
);

CREATE TABLE IF NOT EXISTS relations (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    source_id  TEXT NOT NULL REFERENCES entities(id),
    relation   TEXT NOT NULL,
    target_id  TEXT NOT NULL REFERENCES entities(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(user_id, namespace, source_id, relation, target_id)
);

CREATE INDEX IF NOT EXISTS idx_facts_user ON facts(user_id);
CREATE INDEX IF NOT EXISTS idx_facts_hash ON facts(user_id, hash);
CREATE INDEX IF NOT EXISTS idx_facts_user_ns ON facts(user_id, namespace);
CREATE INDEX IF NOT EXISTS idx_entities_user ON entities(user_id);
CREATE INDEX IF NOT EXISTS idx_entities_user_ns ON entities(user_id, namespace);
CREATE INDEX IF NOT EXISTS idx_entities_cluster ON entities(user_id, namespace, cluster_id);
CREATE INDEX IF NOT EXISTS idx_relations_user ON relations(user_id);
CREATE INDEX IF NOT EXISTS idx_relations_user_ns ON relations(user_id, namespace);
CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);

CREATE TABLE IF NOT EXISTS fact_history (
    id         TEXT PRIMARY KEY,
    fact_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    event      TEXT NOT NULL,
    old_text   TEXT,
    new_text   TEXT,
    old_hash   TEXT,
    new_hash   TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fact_history_fact ON fact_history(fact_id);
CREATE INDEX IF NOT EXISTS idx_fact_history_user ON fact_history(user_id, created_at);

CREATE TABLE IF NOT EXISTS processed_conversations (
    user_id      TEXT NOT NULL,
    namespace    TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY(user_id, namespace, content_hash)
);

CREATE TABLE IF NOT EXISTS profiles (
    user_id    TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    fact_hash  TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(user_id, namespace)
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

	// Additive migration: cluster_id column (safe no-op on fresh DBs)
	db.Exec("ALTER TABLE entities ADD COLUMN cluster_id INTEGER NOT NULL DEFAULT 0")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_entities_cluster ON entities(user_id, namespace, cluster_id)")

	// Additive migration: is_summary column (safe no-op on fresh DBs)
	db.Exec("ALTER TABLE facts ADD COLUMN is_summary INTEGER NOT NULL DEFAULT 0")

	// Create vec0 virtual tables
	for _, tbl := range []string{"fact_embeddings", "entity_embeddings"} {
		vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`, tbl, dimension)
		if _, err := db.Exec(vecSQL); err != nil {
			db.Close()
			return nil, fmt.Errorf("create %s: %w", tbl, err)
		}
	}

	// FTS5 full-text index for BM25 hybrid retrieval (graceful no-op if FTS5 unavailable)
	if err := migrateFTS5(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate fts5: %w", err)
	}

	// Additive migration: importance column
	if _, err := db.Exec("ALTER TABLE facts ADD COLUMN importance INTEGER NOT NULL DEFAULT 3"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate importance: %w", err)
		}
	}

	// Additive migration: bi-temporal columns
	if _, err := db.Exec("ALTER TABLE facts ADD COLUMN valid_from TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate valid_from: %w", err)
		}
	}
	if _, err := db.Exec("ALTER TABLE facts ADD COLUMN invalid_at TEXT"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate invalid_at: %w", err)
		}
	}

	return &Store{db: db, dimension: dimension}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func newID() string {
	return uuid.New().String()
}

// --- Facts ---

func (s *Store) InsertFact(userID, namespace, text, hash string, embedding []float32, importance int) (string, error) {
	if importance <= 0 || importance > 5 {
		importance = 3
	}
	id := newID()
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO facts (id, user_id, namespace, text, hash, importance, created_at, updated_at, valid_from) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, userID, namespace, text, hash, importance, ts, ts, ts); err != nil {
		return "", fmt.Errorf("insert fact: %w", err)
	}

	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO fact_embeddings (id, embedding) VALUES (?, ?)", id, string(embJSON)); err != nil {
		return "", fmt.Errorf("insert embedding: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, namespace, event, new_text, new_hash, created_at) VALUES (?, ?, ?, ?, 'ADD', ?, ?, ?)",
		newID(), id, userID, namespace, text, hash, ts); err != nil {
		return "", fmt.Errorf("record history: %w", err)
	}

	return id, tx.Commit()
}

func (s *Store) UpdateFact(id, text, hash string, embedding []float32) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Capture current state for history
	var oldText, oldHash, userID, namespace string
	var importance int
	if err := tx.QueryRow("SELECT user_id, namespace, text, hash, importance FROM facts WHERE id = ?", id).Scan(&userID, &namespace, &oldText, &oldHash, &importance); err != nil {
		return "", fmt.Errorf("read current fact: %w", err)
	}

	ts := now()

	// Soft-invalidate old version
	if _, err := tx.Exec("UPDATE facts SET invalid_at = ? WHERE id = ?", ts, id); err != nil {
		return "", fmt.Errorf("invalidate old fact: %w", err)
	}
	// Remove old embedding from vec0
	if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
		return "", fmt.Errorf("delete old embedding: %w", err)
	}

	// Create new version
	vid := newID()
	if _, err := tx.Exec("INSERT INTO facts (id, user_id, namespace, text, hash, importance, created_at, updated_at, valid_from) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		vid, userID, namespace, text, hash, importance, ts, ts, ts); err != nil {
		return "", fmt.Errorf("insert new version: %w", err)
	}

	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO fact_embeddings (id, embedding) VALUES (?, ?)", vid, string(embJSON)); err != nil {
		return "", fmt.Errorf("insert new embedding: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, namespace, event, old_text, new_text, old_hash, new_hash, created_at) VALUES (?, ?, ?, ?, 'UPDATE', ?, ?, ?, ?, ?)",
		newID(), vid, userID, namespace, oldText, text, oldHash, hash, ts); err != nil {
		return "", fmt.Errorf("record history: %w", err)
	}

	return vid, tx.Commit()
}

func (s *Store) DeleteFact(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Capture current state for history
	var oldText, oldHash, userID string
	if err := tx.QueryRow("SELECT user_id, text, hash FROM facts WHERE id = ?", id).Scan(&userID, &oldText, &oldHash); err != nil {
		return fmt.Errorf("read current fact: %w", err)
	}

	ts := now()
	// Soft-delete: set invalid_at, remove embedding from vec0
	if _, err := tx.Exec("UPDATE facts SET invalid_at = ? WHERE id = ?", ts, id); err != nil {
		return fmt.Errorf("soft-delete fact: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete embedding: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, event, old_text, old_hash, created_at) VALUES (?, ?, ?, 'DELETE', ?, ?, ?)",
		newID(), id, userID, oldText, oldHash, ts); err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit()
}

func (s *Store) GetFact(id string) (*Fact, error) {
	var f Fact
	err := s.db.QueryRow("SELECT id, user_id, text, hash, created_at, updated_at, access_count, importance, COALESCE(valid_from,''), COALESCE(invalid_at,'') FROM facts WHERE id = ? AND invalid_at IS NULL", id).
		Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &f.Importance, &f.ValidFrom, &f.InvalidAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ReinsertFact restores a soft-deleted fact by clearing invalid_at, or inserts fresh.
func (s *Store) ReinsertFact(id, userID, text, hash string, embedding []float32, importance int) error {
	if importance <= 0 || importance > 5 {
		importance = 3
	}
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Check if the row exists (soft-deleted)
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM facts WHERE id = ?", id).Scan(&count); err != nil {
		return err
	}

	if count > 0 {
		// Row exists (soft-deleted) — restore it
		if _, err := tx.Exec("UPDATE facts SET text = ?, hash = ?, importance = ?, updated_at = ?, invalid_at = NULL, valid_from = ? WHERE id = ?",
			text, hash, importance, ts, ts, id); err != nil {
			return fmt.Errorf("restore fact: %w", err)
		}
	} else {
		if _, err := tx.Exec("INSERT INTO facts (id, user_id, text, hash, importance, created_at, updated_at, valid_from) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			id, userID, text, hash, importance, ts, ts, ts); err != nil {
			return fmt.Errorf("reinsert fact: %w", err)
		}
	}

	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO fact_embeddings (id, embedding) VALUES (?, ?)", id, string(embJSON)); err != nil {
		return fmt.Errorf("reinsert embedding: %w", err)
	}

	return tx.Commit()
}

// GetFactHistory returns all history entries for a fact, newest first.
func (s *Store) GetFactHistory(factID string) ([]FactHistoryEntry, error) {
	rows, err := s.db.Query(
		"SELECT id, fact_id, user_id, event, COALESCE(old_text,''), COALESCE(new_text,''), created_at FROM fact_history WHERE fact_id = ? ORDER BY created_at DESC, rowid DESC",
		factID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []FactHistoryEntry
	for rows.Next() {
		var e FactHistoryEntry
		if err := rows.Scan(&e.ID, &e.FactID, &e.UserID, &e.Event, &e.OldText, &e.NewText, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetLatestHistoryEntry returns the most recent history entry for a fact.
func (s *Store) GetLatestHistoryEntry(factID string) (*FactHistoryEntry, error) {
	var e FactHistoryEntry
	err := s.db.QueryRow(
		"SELECT id, fact_id, user_id, event, COALESCE(old_text,''), COALESCE(new_text,''), created_at FROM fact_history WHERE fact_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1",
		factID).Scan(&e.ID, &e.FactID, &e.UserID, &e.Event, &e.OldText, &e.NewText, &e.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// PruneHistoryForUser deletes history entries older than cutoff for a user, returns count deleted.
func (s *Store) PruneHistoryForUser(userID, namespace string, cutoff time.Time) (int, error) {
	res, err := s.db.Exec(
		"DELETE FROM fact_history WHERE user_id = ? AND namespace = ? AND created_at < ?",
		userID, namespace, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// BumpAccess increments access_count and sets last_accessed_at for the given fact IDs.
func (s *Store) BumpAccess(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("UPDATE facts SET access_count = access_count + 1, last_accessed_at = ? WHERE id = ?", ts, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CountFacts returns the total number of facts for a user+namespace.
func (s *Store) CountFacts(userID, namespace string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE user_id = ? AND namespace = ? AND invalid_at IS NULL AND is_summary = 0", userID, namespace).Scan(&count)
	return count, err
}

func (s *Store) GetAllFacts(userID, namespace string, limit int) ([]Fact, error) {
	rows, err := s.db.Query("SELECT id, user_id, text, hash, created_at, updated_at, access_count, importance FROM facts WHERE user_id = ? AND namespace = ? AND invalid_at IS NULL AND is_summary = 0 ORDER BY created_at DESC LIMIT ?", userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &f.Importance); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func (s *Store) SearchFacts(queryEmbedding []float32, userID, namespace string, limit int) ([]Fact, error) {
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
		SELECT f.id, f.user_id, f.text, f.hash, f.created_at, f.updated_at, f.access_count, f.importance, e.distance
		FROM fact_embeddings e
		JOIN facts f ON f.id = e.id
		WHERE e.embedding MATCH ?
		  AND k = ?
		  AND f.user_id = ?
		  AND f.namespace = ?
		  AND f.invalid_at IS NULL
		  AND f.is_summary = 0
		ORDER BY e.distance
		LIMIT ?
	`, string(embJSON), kFetch, userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var dist float64
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &f.Importance, &dist); err != nil {
			return nil, err
		}
		f.Score = 1 - dist // cosine distance → similarity
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func (s *Store) FactExistsByHash(userID, namespace, hash string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE user_id = ? AND namespace = ? AND hash = ? AND invalid_at IS NULL", userID, namespace, hash).Scan(&count)
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

func (s *Store) DeleteAllForUser(userID, namespace string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete fact embeddings
	factIDs, err := queryIDs(tx, "SELECT id FROM facts WHERE user_id = ? AND namespace = ?", userID, namespace)
	if err != nil {
		return err
	}
	for _, id := range factIDs {
		if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM facts WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM relations WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}

	// Delete entity embeddings
	entIDs, err := queryIDs(tx, "SELECT id FROM entities WHERE user_id = ? AND namespace = ?", userID, namespace)
	if err != nil {
		return err
	}
	for _, id := range entIDs {
		if _, err := tx.Exec("DELETE FROM entity_embeddings WHERE id = ?", id); err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM entities WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM processed_conversations WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM profiles WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM fact_history WHERE user_id = ? AND namespace = ?", userID, namespace); err != nil {
		return err
	}

	return tx.Commit()
}

// CleanupStaleRelations removes relations where the source or target entity name
// appears in deletedTexts but not in any remaining fact for the user.
// Uses SQL-based containment checks instead of loading all facts into memory.
func (s *Store) CleanupStaleRelations(userID, namespace string, deletedTexts []string) (int, error) {
	if len(deletedTexts) == 0 {
		return 0, nil
	}

	// Get entities for this user+namespace
	entities, err := s.GetAllEntities(userID, namespace, 100_000)
	if err != nil {
		return 0, err
	}

	// Find entity names mentioned in deleted texts (Go-side, small set)
	var affectedEntityIDs []string
	for _, e := range entities {
		nameLower := strings.ToLower(e.Name)
		for _, dt := range deletedTexts {
			if strings.Contains(strings.ToLower(dt), nameLower) {
				affectedEntityIDs = append(affectedEntityIDs, e.ID)
				break
			}
		}
	}

	if len(affectedEntityIDs) == 0 {
		return 0, nil
	}

	// Build a set for O(1) lookup
	affected := make(map[string]bool, len(affectedEntityIDs))
	for _, id := range affectedEntityIDs {
		affected[id] = true
	}

	// For each affected entity, check via SQL if it still appears in any remaining fact.
	// This replaces loading ALL facts into memory.
	var orphanedIDs []string
	for _, e := range entities {
		if !affected[e.ID] {
			continue
		}

		var count int
		err := s.db.QueryRow(
			"SELECT COUNT(*) FROM facts WHERE user_id = ? AND namespace = ? AND invalid_at IS NULL AND LOWER(text) LIKE '%' || ? || '%'",
			userID, namespace, strings.ToLower(e.Name),
		).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("check entity %q in facts: %w", e.Name, err)
		}
		if count == 0 {
			orphanedIDs = append(orphanedIDs, e.ID)
		}
	}

	if len(orphanedIDs) == 0 {
		return 0, nil
	}

	// Delete relations involving orphaned entities
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := 0
	for _, id := range orphanedIDs {
		res, err := tx.Exec("DELETE FROM relations WHERE user_id = ? AND namespace = ? AND (source_id = ? OR target_id = ?)", userID, namespace, id, id)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}

	return deleted, tx.Commit()
}

// --- Profiles ---

// GetProfile returns the cached profile for a user, or nil if none exists.
func (s *Store) GetProfile(userID, namespace string) (summary, factHash string, err error) {
	err = s.db.QueryRow("SELECT summary, fact_hash FROM profiles WHERE user_id = ? AND namespace = ?", userID, namespace).Scan(&summary, &factHash)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return
}

// UpsertProfile stores or updates the cached profile for a user+namespace.
func (s *Store) UpsertProfile(userID, namespace, summary, factHash string) error {
	_, err := s.db.Exec(`INSERT INTO profiles (user_id, namespace, summary, fact_hash, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, namespace) DO UPDATE SET summary = ?, fact_hash = ?, updated_at = ?`,
		userID, namespace, summary, factHash, now(), summary, factHash, now())
	return err
}

// --- Conversation Dedup ---

func (s *Store) ConversationExists(userID, namespace, contentHash string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM processed_conversations WHERE user_id = ? AND namespace = ? AND content_hash = ?", userID, namespace, contentHash).Scan(&count)
	return count > 0, err
}

func (s *Store) MarkConversationProcessed(userID, namespace, contentHash string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO processed_conversations (user_id, namespace, content_hash, created_at) VALUES (?, ?, ?, ?)", userID, namespace, contentHash, now())
	return err
}

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

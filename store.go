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
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    access_count    INTEGER NOT NULL DEFAULT 0,
    last_accessed_at TEXT
);

CREATE TABLE IF NOT EXISTS entities (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    namespace  TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
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

	// Create vec0 virtual tables
	for _, tbl := range []string{"fact_embeddings", "entity_embeddings"} {
		vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`, tbl, dimension)
		if _, err := db.Exec(vecSQL); err != nil {
			db.Close()
			return nil, fmt.Errorf("create %s: %w", tbl, err)
		}
	}

	// Migrate: create fact_history table if missing (for DBs created before this schema addition)
	var historyExists int
	_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fact_history'").Scan(&historyExists)
	if historyExists == 0 {
		historyDDL := `
			CREATE TABLE IF NOT EXISTS fact_history (
			    id TEXT PRIMARY KEY, fact_id TEXT NOT NULL, user_id TEXT NOT NULL,
			    event TEXT NOT NULL, old_text TEXT, new_text TEXT, old_hash TEXT, new_hash TEXT,
			    created_at TEXT NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_fact_history_fact ON fact_history(fact_id);
			CREATE INDEX IF NOT EXISTS idx_fact_history_user ON fact_history(user_id, created_at);`
		if _, err := db.Exec(historyDDL); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate fact_history: %w", err)
		}
	}

	// Migrate: add access_count and last_accessed_at to facts if missing
	for _, col := range []struct{ name, ddl string }{
		{"access_count", "ALTER TABLE facts ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0"},
		{"last_accessed_at", "ALTER TABLE facts ADD COLUMN last_accessed_at TEXT"},
	} {
		var exists int
		_ = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('facts') WHERE name = ?", col.name).Scan(&exists)
		if exists == 0 {
			if _, err := db.Exec(col.ddl); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate %s: %w", col.name, err)
			}
		}
	}

	// Migrate: add namespace column to all tables if missing
	nsMigrations := []struct{ table, ddl string }{
		{"facts", "ALTER TABLE facts ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
		{"entities", "ALTER TABLE entities ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
		{"relations", "ALTER TABLE relations ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
		{"fact_history", "ALTER TABLE fact_history ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
		{"processed_conversations", "ALTER TABLE processed_conversations ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
		{"profiles", "ALTER TABLE profiles ADD COLUMN namespace TEXT NOT NULL DEFAULT ''"},
	}
	for _, m := range nsMigrations {
		var exists int
		_ = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = 'namespace'", m.table)).Scan(&exists)
		if exists == 0 {
			if _, err := db.Exec(m.ddl); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate namespace on %s: %w", m.table, err)
			}
		}
	}
	// Create namespace-scoped indexes (idempotent)
	for _, idx := range []string{
		"CREATE INDEX IF NOT EXISTS idx_facts_user_ns ON facts(user_id, namespace)",
		"CREATE INDEX IF NOT EXISTS idx_entities_user_ns ON entities(user_id, namespace)",
		"CREATE INDEX IF NOT EXISTS idx_relations_user_ns ON relations(user_id, namespace)",
	} {
		if _, err := db.Exec(idx); err != nil {
			db.Close()
			return nil, fmt.Errorf("create ns index: %w", err)
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

func (s *Store) InsertFact(userID, namespace, text, hash string, embedding []float32) (string, error) {
	id := newID()
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO facts (id, user_id, namespace, text, hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, userID, namespace, text, hash, ts, ts); err != nil {
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

func (s *Store) UpdateFact(id, text, hash string, embedding []float32) error {
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
	if _, err := tx.Exec("UPDATE facts SET text = ?, hash = ?, updated_at = ? WHERE id = ?", text, hash, ts, id); err != nil {
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

	if _, err := tx.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, event, old_text, new_text, old_hash, new_hash, created_at) VALUES (?, ?, ?, 'UPDATE', ?, ?, ?, ?, ?)",
		newID(), id, userID, oldText, text, oldHash, hash, ts); err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit()
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

	if _, err := tx.Exec("DELETE FROM fact_embeddings WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete embedding: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM facts WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}

	ts := now()
	if _, err := tx.Exec(
		"INSERT INTO fact_history (id, fact_id, user_id, event, old_text, old_hash, created_at) VALUES (?, ?, ?, 'DELETE', ?, ?, ?)",
		newID(), id, userID, oldText, oldHash, ts); err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit()
}

func (s *Store) GetFact(id string) (*Fact, error) {
	var f Fact
	err := s.db.QueryRow("SELECT id, user_id, text, hash, created_at, updated_at, access_count FROM facts WHERE id = ?", id).
		Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ReinsertFact re-inserts a fact with a specific ID (used by Undo after DELETE).
func (s *Store) ReinsertFact(id, userID, text, hash string, embedding []float32) error {
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO facts (id, user_id, text, hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, userID, text, hash, ts, ts); err != nil {
		return fmt.Errorf("reinsert fact: %w", err)
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
		userID, namespace, cutoff.UTC().Format(time.RFC3339))
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

func (s *Store) GetAllFacts(userID, namespace string, limit int) ([]Fact, error) {
	rows, err := s.db.Query("SELECT id, user_id, text, hash, created_at, updated_at, access_count FROM facts WHERE user_id = ? AND namespace = ? ORDER BY created_at DESC LIMIT ?", userID, namespace, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount); err != nil {
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
		SELECT f.id, f.user_id, f.text, f.hash, f.created_at, f.updated_at, f.access_count, e.distance
		FROM fact_embeddings e
		JOIN facts f ON f.id = e.id
		WHERE e.embedding MATCH ?
		  AND k = ?
		  AND f.user_id = ?
		  AND f.namespace = ?
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
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.Hash, &f.CreatedAt, &f.UpdatedAt, &f.AccessCount, &dist); err != nil {
			return nil, err
		}
		f.Score = 1 - dist // cosine distance → similarity
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

func (s *Store) FactExistsByHash(userID, namespace, hash string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM facts WHERE user_id = ? AND namespace = ? AND hash = ?", userID, namespace, hash).Scan(&count)
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
func (s *Store) CleanupStaleRelations(userID, namespace string, deletedTexts []string) (int, error) {
	if len(deletedTexts) == 0 {
		return 0, nil
	}

	// Get all entity names for this user+namespace
	entities, err := s.GetAllEntities(userID, namespace, 100_000)
	if err != nil {
		return 0, err
	}

	// Find entity names mentioned in deleted fact texts
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

	// For each affected entity, check if it still appears in any remaining fact
	remainingFacts, err := s.GetAllFacts(userID, namespace, 100_000)
	if err != nil {
		return 0, err
	}

	// Build a combined text of all remaining facts for quick contains check
	var allText strings.Builder
	for _, f := range remainingFacts {
		allText.WriteString(strings.ToLower(f.Text))
		allText.WriteString(" ")
	}
	remainingText := allText.String()

	// Find entities that no longer appear in any fact
	var orphanedIDs []string
	for _, e := range entities {
		for _, aid := range affectedEntityIDs {
			if e.ID == aid && !strings.Contains(remainingText, strings.ToLower(e.Name)) {
				orphanedIDs = append(orphanedIDs, e.ID)
			}
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

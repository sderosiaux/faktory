package faktory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Export writes all facts, entities, and relations for a user as JSONL.
func (m *Memory) Export(ctx context.Context, userID string, w io.Writer, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	facts, err := m.store.GetAllFacts(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export facts: %w", err)
	}
	for _, f := range facts {
		if err := enc.Encode(ExportRecord{Type: "fact", Text: f.Text}); err != nil {
			return err
		}
	}

	entities, err := m.store.GetAllEntities(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export entities: %w", err)
	}
	for _, e := range entities {
		if err := enc.Encode(ExportRecord{Type: "entity", Name: e.Name, EntityType: e.Type}); err != nil {
			return err
		}
	}

	rels, err := m.store.GetAllRelations(userID, o.namespace, 100_000)
	if err != nil {
		return fmt.Errorf("export relations: %w", err)
	}
	for _, r := range rels {
		if err := enc.Encode(ExportRecord{Type: "relation", Source: r.Source, Relation: r.Relation, Target: r.Target}); err != nil {
			return err
		}
	}

	return nil
}

// Import reads JSONL records and inserts them for a user. Facts and entities
// are embedded on import. Existing data is not cleared first.
func (m *Memory) Import(ctx context.Context, userID string, r io.Reader, opts ...Option) error {
	if userID == "" {
		return fmt.Errorf("user_id is required")
	}
	o := resolveOpts(opts)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var factTexts []string
	var entityRecords []ExportRecord
	var relationRecords []ExportRecord

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec ExportRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("parse record: %w", err)
		}
		switch rec.Type {
		case "fact":
			factTexts = append(factTexts, rec.Text)
		case "entity":
			entityRecords = append(entityRecords, rec)
		case "relation":
			relationRecords = append(relationRecords, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	if len(factTexts) > 0 {
		embs, err := m.embedder.EmbedBatch(ctx, factTexts)
		if err != nil {
			return fmt.Errorf("embed facts: %w", err)
		}
		for i, text := range factTexts {
			if _, err := m.store.InsertFact(userID, o.namespace, text, hashFact(text), embs[i], 3, "", 0); err != nil {
				return fmt.Errorf("insert fact: %w", err)
			}
		}
	}

	if len(entityRecords) > 0 {
		names := make([]string, len(entityRecords))
		for i, rec := range entityRecords {
			names[i] = rec.Name
		}
		embs, err := m.embedder.EmbedBatch(ctx, names)
		if err != nil {
			return fmt.Errorf("embed entities: %w", err)
		}
		for i, rec := range entityRecords {
			id, err := m.store.UpsertEntity(userID, o.namespace, rec.Name, rec.EntityType)
			if err != nil {
				return fmt.Errorf("upsert entity: %w", err)
			}
			if err := m.store.UpsertEntityEmbedding(id, embs[i]); err != nil {
				return fmt.Errorf("store entity embedding: %w", err)
			}
		}
	}

	for _, rec := range relationRecords {
		srcID, err := m.store.UpsertEntity(userID, o.namespace, rec.Source, "other")
		if err != nil {
			return fmt.Errorf("upsert source: %w", err)
		}
		tgtID, err := m.store.UpsertEntity(userID, o.namespace, rec.Target, "other")
		if err != nil {
			return fmt.Errorf("upsert target: %w", err)
		}
		if err := m.store.UpsertRelation(userID, o.namespace, srcID, rec.Relation, tgtID); err != nil {
			return fmt.Errorf("upsert relation: %w", err)
		}
	}

	return nil
}

package faktory

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// nopLogger returns a logger that discards all output.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"` // "user", "assistant", "system"
	Content string `json:"content"`
}

// Fact is a stored atomic fact about a user.
type Fact struct {
	ID          string  `json:"id"`
	UserID      string  `json:"user_id"`
	Text        string  `json:"text"`
	Hash        string  `json:"hash,omitempty"`
	Score       float64 `json:"score,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	AccessCount int     `json:"access_count,omitempty"`
	Importance  int     `json:"importance,omitempty"`
	ValidFrom   string  `json:"valid_from,omitempty"`
	InvalidAt   string  `json:"invalid_at,omitempty"`
}

// Relation is a stored entity-relation-entity triplet.
type Relation struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourceType string `json:"source_type"`
	Relation   string `json:"relation"`
	Target     string `json:"target"`
	TargetType string `json:"target_type"`
}

// EntityRef is a name+type pair extracted from a conversation.
type EntityRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RelationRef is a source-relation-target triplet extracted from a conversation.
type RelationRef struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// AddResult summarizes what happened during an Add() call.
type AddResult struct {
	Added              []Fact        `json:"added,omitempty"`
	Updated            []Fact        `json:"updated,omitempty"`
	Deleted            []string      `json:"deleted,omitempty"`
	Noops              int           `json:"noops,omitempty"`
	Tokens             int           `json:"tokens,omitempty"`
	TotalFacts         int           `json:"total_facts,omitempty"`
	GraphErrors        []string      `json:"graph_errors,omitempty"`
	ExtractedFacts     []string      `json:"extracted_facts,omitempty"`
	ExtractedEntities  []EntityRef   `json:"extracted_entities,omitempty"`
	ExtractedRelations []RelationRef `json:"extracted_relations,omitempty"`
}

// Completer abstracts LLM chat-completion with structured output.
type Completer interface {
	Complete(ctx context.Context, system, user, schemaName string, schema json.RawMessage, result any) (int, error)
	CompleteWithCorrection(ctx context.Context, system, user, previousResponse, correction, schemaName string, schema json.RawMessage, result any) (int, error)
}

// TextEmbedder abstracts text embedding.
type TextEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// FactHistoryEntry records a single mutation event on a fact.
type FactHistoryEntry struct {
	ID        string `json:"id"`
	FactID    string `json:"fact_id"`
	UserID    string `json:"user_id"`
	Event     string `json:"event"` // ADD, UPDATE, DELETE, UNDO
	OldText   string `json:"old_text,omitempty"`
	NewText   string `json:"new_text,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Option configures per-call behavior for Memory methods.
type Option func(*callOptions)

type callOptions struct {
	namespace string
}

// WithNamespace scopes the operation to the given namespace.
func WithNamespace(ns string) Option {
	return func(o *callOptions) { o.namespace = ns }
}

func resolveOpts(opts []Option) callOptions {
	var o callOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// RecallOptions configures the Recall() method.
type RecallOptions struct {
	MaxFacts       int    `json:"max_facts,omitempty"`
	MaxRelations   int    `json:"max_relations,omitempty"`
	IncludeProfile bool   `json:"include_profile,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	Rerank         bool   `json:"rerank,omitempty"` // LLM re-rank retrieved facts (adds 1 LLM call)
}

// RecallResult combines facts and relations into a single response
// with a pre-formatted summary ready for system prompt injection.
type RecallResult struct {
	Facts     []Fact     `json:"facts"`
	Relations []Relation `json:"relations"`
	Summary   string     `json:"summary"`
}

// Entity is a stored named entity (person, org, place, etc.).
type Entity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// ExportRecord is a single line in a JSONL export file.
type ExportRecord struct {
	Type       string `json:"type"`                  // "fact", "entity", "relation"
	Text       string `json:"text,omitempty"`        // facts
	Name       string `json:"name,omitempty"`        // entities
	EntityType string `json:"entity_type,omitempty"` // entities
	Source     string `json:"source,omitempty"`      // relations
	Relation   string `json:"relation,omitempty"`    // relations
	Target     string `json:"target,omitempty"`      // relations
}

// Config holds all configuration for a Memory instance.
type Config struct {
	DBPath         string        // Path to SQLite database file
	LLMBaseURL     string        // OpenAI-compatible API base URL (e.g., "https://api.openai.com/v1")
	LLMAPIKey      string        // API key for LLM
	LLMModel       string        // Model name for chat completions (e.g., "gpt-4o-mini")
	EmbedModel     string        // Model name for embeddings (e.g., "text-embedding-3-small")
	EmbedDimension int           // Embedding vector dimension (default: 256, Matryoshka truncation)
	Logger         *slog.Logger  // Structured logger (default: silent)
	HTTPTimeout    time.Duration // Override default 30s timeout (0 = use default)
	HTTPClient     *http.Client  // Fully custom HTTP client (skips timeout and retry when set)
	Completer      Completer     // Custom LLM completer (overrides LLM* fields when set)
	TextEmbedder   TextEmbedder  // Custom text embedder (overrides Embed* fields when set)

	DisableGraph bool // Skip entity/relation extraction in Add() (saves 1 LLM call)

	DecayAlpha float64 // Age decay rate (default: 0.01). Higher = faster decay of old facts.
	DecayBeta  float64 // Access boost rate (default: 0.1). Higher = stronger boost for frequently accessed facts.
	BM25Weight float64 // Weight for BM25 in hybrid score fusion (default: 0.3). 0 = vector only, 1 = BM25 only.

	PromptFactExtraction   string // Override fact extraction system prompt
	PromptReconciliation   string // Override reconciliation system prompt
	PromptEntityExtraction string // Override entity extraction system prompt
}

const defaultHTTPTimeout = 30 * time.Second

// buildHTTPClient returns the HTTP client to use. If HTTPClient is set, it is
// returned as-is. Otherwise a new client is created with the configured (or
// default 30s) timeout.
func (c Config) buildHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	t := c.HTTPTimeout
	if t == 0 {
		t = defaultHTTPTimeout
	}
	return &http.Client{Timeout: t}
}

func (c Config) withDefaults() Config {
	if c.DBPath == "" {
		c.DBPath = "faktory.db"
	}
	if c.LLMBaseURL == "" {
		c.LLMBaseURL = "https://api.openai.com/v1"
	}
	if c.LLMModel == "" {
		c.LLMModel = "gpt-4o-mini"
	}
	if c.EmbedModel == "" {
		c.EmbedModel = "text-embedding-3-small"
	}
	if c.EmbedDimension == 0 {
		c.EmbedDimension = 256
	}
	if c.DecayAlpha == 0 {
		c.DecayAlpha = 0.01
	}
	if c.DecayBeta == 0 {
		c.DecayBeta = 0.1
	}
	if c.BM25Weight == 0 {
		c.BM25Weight = 0.3
	}
	if c.Logger == nil {
		c.Logger = nopLogger()
	}
	return c
}

package faktory

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"`    // "user", "assistant", "system"
	Content string `json:"content"`
}

// Fact is a stored atomic fact about a user.
type Fact struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	Text      string  `json:"text"`
	Hash      string  `json:"hash,omitempty"`
	Score     float64 `json:"score,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
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

// AddResult summarizes what happened during an Add() call.
type AddResult struct {
	Added   []Fact   `json:"added,omitempty"`
	Updated []Fact   `json:"updated,omitempty"`
	Deleted []string `json:"deleted,omitempty"`
	Noops   int      `json:"noops,omitempty"`
}

// Config holds all configuration for a Memory instance.
type Config struct {
	DBPath         string // Path to SQLite database file
	LLMBaseURL     string // OpenAI-compatible API base URL (e.g., "https://api.openai.com/v1")
	LLMAPIKey      string // API key for LLM
	LLMModel       string // Model name for chat completions (e.g., "gpt-4o-mini")
	EmbedModel     string // Model name for embeddings (e.g., "text-embedding-3-small")
	EmbedDimension int    // Embedding vector dimension (e.g., 1536)
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
		c.EmbedDimension = 1536
	}
	return c
}

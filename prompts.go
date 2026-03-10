package faktory

import "encoding/json"

// --- Prompt texts ---

const factExtractionPrompt = `You extract discrete facts from conversations.

Rules:
- Extract facts from user messages only. Ignore assistant and system messages.
- Each fact is a short, self-contained statement.
- Use the same language as the input.
- No opinions about the conversation. No meta-commentary.
- If nothing worth remembering, return an empty list.
- Focus on: preferences, personal details, plans, professional info, relationships.`

const reconcilePrompt = `You manage a fact store. Given existing facts and newly extracted facts, decide what to do with each.

Operations:
- ADD: new information not present in existing facts. Generate a new id starting from the next available integer.
- UPDATE: corrects, enriches, or supersedes an existing fact. Keep same id.
- DELETE: contradicts an existing fact and should be removed. Keep same id.
- NOOP: already known. Keep same id.

Rules:
- Same meaning expressed differently = NOOP (e.g., "likes pizza" vs "enjoys pizza").
- New value replaces old = UPDATE (e.g., "lives in Paris" then "moved to Lyon" = UPDATE to "lives in Lyon").
- Direct contradiction with no replacement = DELETE old + ADD new.
- Preserve the language of the facts.
- Only use IDs from the existing facts list. Generate new integer IDs only for ADD.`

const entityExtractionPrompt = `Extract entities and their relationships from the conversation.

Entity types: person, organization, place, product, event, concept, other.
Relations: use short snake_case verbs (works_at, lives_in, likes, owns, married_to, ...).

Rules:
- Only extract what is explicitly stated or strongly implied.
- Do not invent relations.
- Normalize entity names: capitalize proper nouns.
- Use the same language as the input for entity names.`

// --- Response types ---

type FactExtractionResult struct {
	Facts []string `json:"facts"`
}

type ReconcileAction struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Event     string  `json:"event"` // ADD, UPDATE, DELETE, NOOP
	OldMemory *string `json:"old_memory"`
}

type ReconcileResult struct {
	Memory []ReconcileAction `json:"memory"`
}

type ExtractedEntity struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ExtractedRelation struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target  string `json:"target"`
}

type EntityExtractionResult struct {
	Entities  []ExtractedEntity  `json:"entities"`
	Relations []ExtractedRelation `json:"relations"`
}

// --- JSON Schemas for structured output ---

var factExtractionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "facts": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "required": ["facts"],
  "additionalProperties": false
}`)

var reconcileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "memory": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "text": {"type": "string"},
          "event": {"type": "string", "enum": ["ADD", "UPDATE", "DELETE", "NOOP"]},
          "old_memory": {"type": ["string", "null"]}
        },
        "required": ["id", "text", "event", "old_memory"],
        "additionalProperties": false
      }
    }
  },
  "required": ["memory"],
  "additionalProperties": false
}`)

var entityExtractionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "type": {"type": "string"}
        },
        "required": ["name", "type"],
        "additionalProperties": false
      }
    },
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "source": {"type": "string"},
          "relation": {"type": "string"},
          "target": {"type": "string"}
        },
        "required": ["source", "relation", "target"],
        "additionalProperties": false
      }
    }
  },
  "required": ["entities", "relations"],
  "additionalProperties": false
}`)

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
- Focus on: preferences, personal details, plans, professional info, relationships.
- When a user mentions switching or changing something (e.g., "switched from X to Y"), extract the NEW state as a standalone fact (e.g., "Uses Y"), not the transition.
- Extract ALL facts, even minor ones mentioned alongside bigger news. Do not skip secondary details.
- Rate each fact's importance from 1 to 5:
  1 = trivial/transient (e.g., "the weather is nice today")
  2 = minor preference or detail
  3 = standard personal fact (default)
  4 = significant life detail (job, location, relationship)
  5 = core identity or critical info`

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

Step 1: In "resolved_text", copy the user messages as-is.
Step 2: List ALL entities found in the user messages.
Step 3: For EACH pair of entities, determine if a relation exists.

Entity types (use exactly one): person, organization, place, product, event, concept, other.

Relation types (use exactly one):
  People: knows, friend_of, sibling_of, parent_of, child_of, married_to, partner_of, colleague_of, mentored_by
  Work: works_at, manages, reports_to, founded, member_of, hired_by, contracted_by
  Location: lives_in, born_in, located_in, moved_to, visited, from
  Education: studied_at, graduated_from, teaches_at
  Ownership: owns, uses, created, built, authored, developed
  Preference: likes, dislikes, prefers, interested_in, allergic_to
  Language: speaks, reads, writes
  General: part_of, related_to, instance_of, has, wants, plans_to

If none of the above fits, pick the closest one. Do NOT invent new relation types.

Rules:
- Only extract from user messages. Ignore assistant responses.
- Only extract what is explicitly stated or strongly implied.
- Do not invent relations.
- Normalize entity names: capitalize proper nouns (e.g., "alice" → "Alice").
- Never use pronouns as entity names (no "I", "he", "she", "my", "they").
- Use the same language as the input for entity names.
- Be thorough: extract ALL entities and relations, including pets, allergies, tools, and hobbies.
- Each person-entity connection deserves its own relation.
- When items are listed together (e.g., "I use Python and Rust"), create a SEPARATE relation for EACH item.

Example:
Input: "I'm Alice. I have a cat named Mochi. My boyfriend Tom is allergic to cats. He works at Figma. I use Python and Rust."
resolved_text: "Alice has a cat named Mochi. Alice's boyfriend Tom is allergic to cats. Tom works at Figma. Alice uses Python and Rust."
entities: Alice (person), Mochi (other), Tom (person), Figma (organization), Python (product), Rust (product)
relations: Alice owns Mochi, Alice partner_of Tom, Tom allergic_to cats, Tom works_at Figma, Alice uses Python, Alice uses Rust`

const sessionSummaryPrompt = `Summarize the following conversation into 2-3 sentences.
Focus on: what was discussed, key decisions made, action items, and notable information shared.
Write from the perspective of what the user shared or asked about.
Use the same language as the conversation.`

var sessionSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string"}
  },
  "required": ["summary"],
  "additionalProperties": false
}`)

// SessionSummaryResult is the structured output from session summary generation.
type SessionSummaryResult struct {
	Summary string `json:"summary"`
}

const profileGenerationPrompt = `Summarize everything known about this user into a concise profile.

Group by: personal details, work/education, preferences, relationships, plans.
Skip empty groups. Use natural prose, not bullet points.
Be concise — under 300 words. Use the same language as the facts.`

var profileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "profile": {"type": "string"}
  },
  "required": ["profile"],
  "additionalProperties": false
}`)

// --- Response types ---

// ExtractedFact is a single fact with its importance rating and optional qualifiers.
type ExtractedFact struct {
	Text       string `json:"text"`
	Importance int    `json:"importance"`
	Source     string `json:"source,omitempty"`
	Confidence int    `json:"confidence,omitempty"`
}

type FactExtractionResult struct {
	Facts []ExtractedFact `json:"facts"`
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
	Target   string `json:"target"`
}

type EntityExtractionResult struct {
	ResolvedText string              `json:"resolved_text"`
	Entities     []ExtractedEntity   `json:"entities"`
	Relations    []ExtractedRelation `json:"relations"`
}

// --- JSON Schemas for structured output ---

var factExtractionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "facts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "importance": {"type": "integer", "minimum": 1, "maximum": 5}
        },
        "required": ["text", "importance"],
        "additionalProperties": false
      }
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
    "resolved_text": {"type": "string"},
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "type": {"type": "string", "enum": ["person", "organization", "place", "product", "event", "concept", "other"]}
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
          "relation": {"type": "string", "enum": [
            "knows", "friend_of", "sibling_of", "parent_of", "child_of", "married_to", "partner_of", "colleague_of", "mentored_by",
            "works_at", "manages", "reports_to", "founded", "member_of", "hired_by", "contracted_by",
            "lives_in", "born_in", "located_in", "moved_to", "visited", "from",
            "studied_at", "graduated_from", "teaches_at",
            "owns", "uses", "created", "built", "authored", "developed",
            "likes", "dislikes", "prefers", "interested_in", "allergic_to",
            "speaks", "reads", "writes",
            "part_of", "related_to", "instance_of", "has", "wants", "plans_to"
          ]},
          "target": {"type": "string"}
        },
        "required": ["source", "relation", "target"],
        "additionalProperties": false
      }
    }
  },
  "required": ["resolved_text", "entities", "relations"],
  "additionalProperties": false
}`)

// --- Rerank ---

const rerankPrompt = `Given a query and a list of facts, re-rank the facts by relevance to the query.
Return the fact IDs in order from most relevant to least relevant.
Only include facts that are actually relevant to the query. Omit irrelevant facts.`

var rerankSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "ranked_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Fact IDs ordered by relevance, most relevant first"
    }
  },
  "required": ["ranked_ids"],
  "additionalProperties": false
}`)

// RerankResult holds the LLM response for re-ranking.
type RerankResult struct {
	RankedIDs []string `json:"ranked_ids"`
}

// --- Qualifier-extended variants (used when Config.EnableQualifiers is true) ---

const qualifierPromptSuffix = `
- Optionally, for each fact:
  - "source": who stated it — "user" if directly stated, "inferred" if derived from context
  - "confidence": how certain is this fact, 1 (guess) to 5 (explicitly stated)
  If unsure, omit these fields.`

var qualifierFactExtractionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "facts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "importance": {"type": "integer", "minimum": 1, "maximum": 5},
          "source": {"type": "string"},
          "confidence": {"type": "integer", "minimum": 1, "maximum": 5}
        },
        "required": ["text", "importance"],
        "additionalProperties": false
      }
    }
  },
  "required": ["facts"],
  "additionalProperties": false
}`)

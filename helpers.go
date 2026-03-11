package faktory

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
)

// maxMessageChars is the approximate character budget for messages (~25K tokens).
const maxMessageChars = 100_000

// applyDecay re-scores facts using temporal decay and access frequency, then sorts descending.
// alpha controls age decay rate (higher = faster decay). beta controls access boost (higher = stronger boost).
func applyDecay(facts []Fact, alpha, beta float64) {
	now := time.Now()
	for i := range facts {
		ageDays := 0.0
		if t, err := time.Parse(time.RFC3339, facts[i].CreatedAt); err == nil {
			ageDays = now.Sub(t).Hours() / 24
		}
		ageFactor := 1.0 / (1.0 + alpha*ageDays)
		accessFactor := 1.0 + beta*math.Log1p(float64(facts[i].AccessCount))
		imp := facts[i].Importance
		if imp == 0 {
			imp = 3
		}
		importanceFactor := 1.0 + 0.2*float64(imp-3)
		facts[i].Score = facts[i].Score * ageFactor * accessFactor * importanceFactor
	}
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Score > facts[j].Score
	})
}

// truncateMessages keeps the last N messages that fit within maxChars.
// Always keeps at least 1 message.
func truncateMessages(logger *slog.Logger, messages []Message, maxChars int) []Message {
	total := 0
	for _, m := range messages {
		total += len(m.Role) + len(m.Content) + 3 // "role: content\n"
	}
	if total <= maxChars {
		return messages
	}

	budget := maxChars
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		cost := len(messages[i].Role) + len(messages[i].Content) + 3
		if budget-cost < 0 && start < len(messages) {
			break
		}
		budget -= cost
		start = i
	}
	logger.Warn("truncating conversation", "from", len(messages), "to", len(messages)-start, "chars", total, "limit", maxChars)
	return messages[start:]
}

func hashFact(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)
}

func formatMessages(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatUserMessages returns only user messages (filters out assistant/system noise).
func formatUserMessages(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		sb.WriteString("user: ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// entityKey returns a case-insensitive lookup key for entity deduplication.
func entityKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// isPronoun returns true if the name is a pronoun that shouldn't be an entity.
var pronouns = map[string]bool{
	"i": true, "me": true, "my": true, "mine": true, "myself": true,
	"he": true, "him": true, "his": true, "himself": true,
	"she": true, "her": true, "hers": true, "herself": true,
	"it": true, "its": true, "itself": true,
	"we": true, "us": true, "our": true, "ours": true, "ourselves": true,
	"they": true, "them": true, "their": true, "theirs": true, "themselves": true,
	"you": true, "your": true, "yours": true, "yourself": true,
	"user": true,
}

func isPronoun(name string) bool {
	return pronouns[strings.ToLower(strings.TrimSpace(name))]
}

// profileFactHash produces a deterministic hash of all fact texts for cache invalidation.
func profileFactHash(facts []Fact) string {
	ids := make([]string, len(facts))
	for i, f := range facts {
		ids[i] = f.ID + ":" + f.Text
	}
	sort.Strings(ids)
	h := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return fmt.Sprintf("%x", h)
}

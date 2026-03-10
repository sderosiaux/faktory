package faktory

import (
	"fmt"
	"strings"
)

// extractionIssues holds categorized validation results.
type extractionIssues struct {
	errors   []string // serious: trigger a repass
	warnings []string // cosmetic: log but don't retry
}

// validateExtraction checks an entity extraction result for rule violations.
func validateExtraction(e *EntityExtractionResult) extractionIssues {
	var issues extractionIssues

	// Build entity name set for referential integrity checks
	entityNames := make(map[string]bool)
	for _, ent := range e.Entities {
		entityNames[entityKey(ent.Name)] = true
	}

	// Error: resolved_text must not be empty
	if strings.TrimSpace(e.ResolvedText) == "" {
		issues.errors = append(issues.errors, "resolved_text is empty — you must rewrite user messages with pronouns resolved")
	}

	// Error: no pronoun entity names
	for _, ent := range e.Entities {
		if isPronoun(ent.Name) {
			issues.errors = append(issues.errors, fmt.Sprintf("entity %q is a pronoun — resolve it to a concrete name", ent.Name))
		}
		if strings.TrimSpace(ent.Name) == "" {
			issues.errors = append(issues.errors, "entity has an empty name")
		}
	}

	// Error: relation source/target must not be a pronoun
	for _, r := range e.Relations {
		if isPronoun(r.Source) {
			issues.errors = append(issues.errors, fmt.Sprintf("relation source %q is a pronoun — use the resolved entity name", r.Source))
		}
		if isPronoun(r.Target) {
			issues.errors = append(issues.errors, fmt.Sprintf("relation target %q is a pronoun — use the resolved entity name", r.Target))
		}
	}

	// Error: no self-referential relations
	for _, r := range e.Relations {
		if entityKey(r.Source) == entityKey(r.Target) {
			issues.errors = append(issues.errors, fmt.Sprintf("relation %s --%s--> %s is self-referential", r.Source, r.Relation, r.Target))
		}
	}

	// Error: duplicate entities (same name, different case)
	seen := make(map[string]string)
	for _, ent := range e.Entities {
		k := entityKey(ent.Name)
		if prev, exists := seen[k]; exists && prev != ent.Name {
			issues.errors = append(issues.errors, fmt.Sprintf("duplicate entity: %q and %q are the same — use consistent casing", prev, ent.Name))
		}
		seen[k] = ent.Name
	}

	// Warning: relation source/target not in entities list (handled by auto-create)
	for _, r := range e.Relations {
		if !entityNames[entityKey(r.Source)] && !isPronoun(r.Source) {
			issues.warnings = append(issues.warnings, fmt.Sprintf("relation source %q not in entities list", r.Source))
		}
		if !entityNames[entityKey(r.Target)] && !isPronoun(r.Target) {
			issues.warnings = append(issues.warnings, fmt.Sprintf("relation target %q not in entities list", r.Target))
		}
	}

	return issues
}

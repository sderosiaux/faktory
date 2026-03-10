//go:build integration

package faktory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// --- Prompt variants for ablation ---

// verboseCotEntityPrompt: adds back verbose pronoun-resolution CoT instruction (old default).
var verboseCotEntityPrompt = strings.Replace(
	strings.Replace(entityExtractionPrompt,
		`Step 1: In "resolved_text", copy the user messages as-is.`,
		`Step 1: In "resolved_text", rewrite ONLY the user messages replacing ALL pronouns (he, she, it, they, we, my, his, her, their), anaphora, and deictic references (there, here, this) with their concrete referents from context. Only resolve when the referent is unambiguous.`,
		1),
	"found in the user messages", "found in the resolved text", 1)

// noFewshotEntityPrompt: removes the Example section.
var noFewshotEntityPrompt = func() string {
	idx := strings.Index(entityExtractionPrompt, "\n\nExample:")
	if idx < 0 {
		return entityExtractionPrompt
	}
	return entityExtractionPrompt[:idx]
}()

// noCompoundEntityPrompt: removes the compound-splitting instruction.
var noCompoundEntityPrompt = strings.Replace(entityExtractionPrompt,
	"- When items are listed together (e.g., \"I use Python and Rust\"), create a SEPARATE relation for EACH item.", "", 1)

// --- Ablation harness ---

type ablationCfg struct {
	name           string
	entityPrompt   string
	filterUserOnly bool
	runValidator   bool
	filterPronouns bool
	temperature    float64
	model          string
}

type ablationResult struct {
	factRecall float64
	relRecall  float64
	falsePos   int
	invalidRel int
	entities   int
	relations  int
	facts      int
	tokens     int
}

// TestAblation runs entity+fact extraction with each knob toggled off
// and reports a comparison table. Requires OPENAI_API_KEY.
//
// Knobs tested:
//  1. resolved_text CoT (pronoun resolution step in prompt)
//  2. Few-shot example (Example section in prompt)
//  3. Compound splitting instruction
//  4. Assistant message filtering (user-only vs all messages)
//  5. Validator + correction loop
//  6. Pronoun filter (post-processing)
//  7. Temperature (0 vs 0.2)
func TestAblation(t *testing.T) {
	skipIfNoKey(t)

	cfgs := []ablationCfg{
		{"baseline", entityExtractionPrompt, true, true, true, 0, "gpt-4o-mini"},
		{"verbose-cot", verboseCotEntityPrompt, true, true, true, 0, "gpt-4o-mini"},
		{"no-fewshot", noFewshotEntityPrompt, true, true, true, 0, "gpt-4o-mini"},
		{"no-compound", noCompoundEntityPrompt, true, true, true, 0, "gpt-4o-mini"},
		{"all-messages", entityExtractionPrompt, false, true, true, 0, "gpt-4o-mini"},
		{"no-validator", entityExtractionPrompt, true, false, true, 0, "gpt-4o-mini"},
		{"no-pronoun-flt", entityExtractionPrompt, true, true, false, 0, "gpt-4o-mini"},
		{"temp-0.2", entityExtractionPrompt, true, true, true, 0.2, "gpt-4o-mini"},
	}

	type row struct {
		cfg ablationCfg
		res ablationResult
		ok  bool
	}
	rows := make([]row, len(cfgs))

	for i, cfg := range cfgs {
		rows[i].cfg = cfg
		t.Run(cfg.name, func(t *testing.T) {
			r, err := runAblationTrial(t, cfg)
			if err != nil {
				t.Errorf("failed: %v", err)
				return
			}
			rows[i].res = r
			rows[i].ok = true
			t.Logf("FactR=%3.0f%%  RelR=%3.0f%%  FP=%d  Inv=%d  Ents=%d  Rels=%d  Facts=%d  Tok=%d",
				r.factRecall, r.relRecall, r.falsePos, r.invalidRel, r.entities, r.relations, r.facts, r.tokens)
		})
	}

	// Summary table
	t.Logf("\n%s", strings.Repeat("=", 75))
	t.Logf("  ABLATION STUDY — Marginal impact of each feature")
	t.Logf("%s", strings.Repeat("=", 75))
	t.Logf("%-18s | %5s | %5s | %2s | %3s | %4s | %6s",
		"Config", "FactR", "RelR", "FP", "Inv", "Rels", "Tokens")
	t.Logf("%s", strings.Repeat("-", 75))

	var baseRelR, baseFactR float64
	for _, r := range rows {
		if !r.ok {
			t.Logf("%-18s | %s", r.cfg.name, "FAILED")
			continue
		}
		if r.cfg.name == "baseline" {
			baseRelR = r.res.relRecall
			baseFactR = r.res.factRecall
		}

		relDelta := ""
		factDelta := ""
		if r.cfg.name != "baseline" {
			rd := r.res.relRecall - baseRelR
			fd := r.res.factRecall - baseFactR
			if rd != 0 {
				relDelta = fmt.Sprintf(" (%+.0f)", rd)
			}
			if fd != 0 {
				factDelta = fmt.Sprintf(" (%+.0f)", fd)
			}
		}

		t.Logf("%-18s | %3.0f%%%s | %3.0f%%%s | %2d | %3d | %4d | %6d",
			r.cfg.name,
			r.res.factRecall, factDelta,
			r.res.relRecall, relDelta,
			r.res.falsePos, r.res.invalidRel, r.res.relations, r.res.tokens)
	}
	t.Logf("%s", strings.Repeat("=", 75))
}

func runAblationTrial(t *testing.T, cfg ablationCfg) (ablationResult, error) {
	t.Helper()

	llm := NewLLM("https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"), cfg.model, nil)
	llm.temperature = cfg.temperature
	ctx := context.Background()
	totalTokens := 0

	// --- Entity extraction ---
	var userContent string
	if cfg.filterUserOnly {
		userContent = formatUserMessages(complexConversation)
	} else {
		userContent = formatMessages(complexConversation)
	}

	var extraction EntityExtractionResult
	tokens, err := llm.Complete(ctx, cfg.entityPrompt, userContent, "entity_extraction", entityExtractionSchema, &extraction)
	totalTokens += tokens
	if err != nil {
		return ablationResult{}, fmt.Errorf("entity extraction: %w", err)
	}

	// Optional: validate + correct
	if cfg.runValidator {
		issues := validateExtraction(&extraction)
		if len(issues.errors) > 0 {
			prevJSON, _ := json.Marshal(extraction)
			correction := fmt.Sprintf(
				"Your previous extraction has %d issues:\n- %s\n\nPlease fix ALL issues.",
				len(issues.errors), strings.Join(issues.errors, "\n- "))
			var corrected EntityExtractionResult
			retryTokens, retryErr := llm.CompleteWithCorrection(
				ctx, cfg.entityPrompt, userContent, string(prevJSON), correction,
				"entity_extraction", entityExtractionSchema, &corrected)
			totalTokens += retryTokens
			if retryErr == nil {
				newIssues := validateExtraction(&corrected)
				if len(newIssues.errors) < len(issues.errors) {
					extraction = corrected
				}
			}
		}
	}

	// Optional: filter pronouns from relations
	if cfg.filterPronouns {
		var filtered []ExtractedRelation
		for _, r := range extraction.Relations {
			if !isPronoun(r.Source) && !isPronoun(r.Target) {
				filtered = append(filtered, r)
			}
		}
		extraction.Relations = filtered
	}

	// Score relations
	var rels []Relation
	for _, r := range extraction.Relations {
		rels = append(rels, Relation{Source: r.Source, Relation: r.Relation, Target: r.Target})
	}

	relHits := 0
	for _, er := range expectedRelations {
		if matchRelation(rels, er) {
			relHits++
		}
	}

	invalidRel := 0
	allowed := map[string]bool{
		"knows": true, "friend_of": true, "sibling_of": true, "parent_of": true, "child_of": true,
		"married_to": true, "partner_of": true, "colleague_of": true, "mentored_by": true,
		"works_at": true, "manages": true, "reports_to": true, "founded": true, "member_of": true,
		"hired_by": true, "contracted_by": true,
		"lives_in": true, "born_in": true, "located_in": true, "moved_to": true, "visited": true, "from": true,
		"studied_at": true, "graduated_from": true, "teaches_at": true,
		"owns": true, "uses": true, "created": true, "built": true, "authored": true, "developed": true,
		"likes": true, "dislikes": true, "prefers": true, "interested_in": true, "allergic_to": true,
		"speaks": true, "reads": true, "writes": true,
		"part_of": true, "related_to": true, "instance_of": true, "has": true, "wants": true, "plans_to": true,
	}
	for _, r := range rels {
		if !allowed[r.Relation] {
			invalidRel++
		}
	}

	// --- Fact extraction ---
	var factResult FactExtractionResult
	factTokens, err := llm.Complete(ctx, factExtractionPrompt, formatMessages(complexConversation),
		"fact_extraction", factExtractionSchema, &factResult)
	totalTokens += factTokens
	if err != nil {
		return ablationResult{}, fmt.Errorf("fact extraction: %w", err)
	}

	var facts []Fact
	for _, f := range factResult.Facts {
		facts = append(facts, Fact{Text: f})
	}

	factHits := 0
	for _, ef := range expectedFacts {
		if matchFact(facts, ef.matchAny) != "" {
			factHits++
		}
	}

	falsePos := 0
	for _, nf := range negativeFacts {
		for _, f := range facts {
			if matchesAll(f.Text, nf.matchAll) {
				falsePos++
			}
		}
	}

	return ablationResult{
		factRecall: float64(factHits) / float64(len(expectedFacts)) * 100,
		relRecall:  float64(relHits) / float64(len(expectedRelations)) * 100,
		falsePos:   falsePos,
		invalidRel: invalidRel,
		entities:   len(extraction.Entities),
		relations:  len(rels),
		facts:      len(facts),
		tokens:     totalTokens,
	}, nil
}

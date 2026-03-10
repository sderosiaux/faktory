package faktory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// expectedFact defines a fact we expect to be extracted.
// matchAny contains substrings — at least one must appear in a stored fact.
type expectedFact struct {
	label    string   // human-readable description
	matchAny []string // case-insensitive substrings: fact matches if ANY is found
}

// expectedRelation defines a relation we expect to be extracted.
type expectedRelation struct {
	label    string   // human-readable description
	source   string   // case-insensitive substring match on source entity
	target   string   // case-insensitive substring match on target entity
	relation []string // acceptable relation types (any match)
}

// negFact defines something that should NOT appear in facts (false positive check).
type negFact struct {
	label    string   // human-readable description
	matchAll []string // case-insensitive: if ALL substrings found in a single fact → false positive
}

// --- Complex conversation scenario ---

var complexConversation = []Message{
	{Role: "user", Content: "Hey! I'm Sophie, nice to meet you."},
	{Role: "assistant", Content: "Hi Sophie! Nice to meet you too. Tell me about yourself!"},
	{Role: "user", Content: "I'm 28 years old, I'm a machine learning engineer at DeepMind in London. I studied computer science at ETH Zurich."},
	{Role: "assistant", Content: "That's amazing! DeepMind is doing incredible work. How long have you been there?"},
	{Role: "user", Content: "About 3 years now. Before that I did an internship at Google Brain in Mountain View. My manager at DeepMind is Dr. Sarah Chen, she's brilliant."},
	{Role: "assistant", Content: "Great trajectory! Do you have any hobbies outside of work?"},
	{Role: "user", Content: "I love rock climbing — I go to the climbing gym 3 times a week. I'm also learning Japanese, been studying for 2 years now. Oh and I'm vegetarian."},
	{Role: "assistant", Content: "Nice! Do you have any pets?"},
	{Role: "user", Content: "Yes! I have a cat named Mochi. My boyfriend Tom is allergic to cats though, so we have an air purifier in every room lol."},
	{Role: "assistant", Content: "Poor Tom! Where do you two live?"},
	{Role: "user", Content: "We live together in Shoreditch, East London. We've been together for 4 years. He works as a product designer at Figma."},
	{Role: "assistant", Content: "Sounds like a nice area! Any travel plans?"},
	{Role: "user", Content: "We're planning a trip to Tokyo in March — perfect since I'm learning Japanese! My sister Emma lives there, she teaches English at a school in Shibuya."},
	{Role: "assistant", Content: "That'll be great practice! Anything else you'd like to share?"},
	{Role: "user", Content: "I use Arch Linux btw. My main programming languages are Python and Rust. I also contribute to the PyTorch project on weekends. Oh and I'm allergic to peanuts — found that out the hard way last year."},
}

var expectedFacts = []expectedFact{
	{"name is Sophie", []string{"sophie", "Sophie"}},
	{"28 years old", []string{"28"}},
	{"ML engineer", []string{"machine learning", "ML", "engineer"}},
	{"works at DeepMind", []string{"deepmind", "DeepMind"}},
	{"lives in London", []string{"london", "London", "Shoreditch"}},
	{"studied at ETH Zurich", []string{"ETH", "Zurich"}},
	{"computer science", []string{"computer science"}},
	{"3 years at DeepMind", []string{"3 year"}},
	{"internship at Google Brain", []string{"Google Brain", "internship"}},
	{"manager is Dr. Sarah Chen", []string{"Sarah Chen", "manager"}},
	{"loves rock climbing", []string{"climbing", "rock"}},
	{"climbing gym 3x/week", []string{"3", "climbing"}},
	{"learning Japanese", []string{"Japanese", "japanese"}},
	{"vegetarian", []string{"vegetarian"}},
	{"has a cat named Mochi", []string{"Mochi", "cat"}},
	{"boyfriend is Tom", []string{"Tom", "boyfriend", "partner"}},
	{"Tom allergic to cats", []string{"Tom", "allergic", "cat"}},
	{"lives in Shoreditch", []string{"Shoreditch", "shoreditch"}},
	{"together 4 years", []string{"4 year"}},
	{"Tom works at Figma", []string{"Figma", "Tom"}},
	{"trip to Tokyo in March", []string{"Tokyo", "tokyo"}},
	{"sister Emma", []string{"Emma", "sister"}},
	{"Emma lives in Tokyo", []string{"Emma", "Tokyo"}},
	{"Emma teaches English", []string{"Emma", "teach"}},
	{"uses Arch Linux", []string{"Arch", "Linux"}},
	{"programs in Python", []string{"Python", "python"}},
	{"programs in Rust", []string{"Rust", "rust"}},
	{"contributes to PyTorch", []string{"PyTorch", "pytorch"}},
	{"allergic to peanuts", []string{"peanut", "allergic"}},
}

var expectedRelations = []expectedRelation{
	{"Sophie works at DeepMind", "sophie", "deepmind", []string{"works_at"}},
	{"Sophie lives in London/Shoreditch", "sophie", "shoreditch", []string{"lives_in"}},
	{"Sophie studied at ETH Zurich", "sophie", "eth", []string{"studied_at", "graduated_from"}},
	{"Sophie likes climbing", "sophie", "climbing", []string{"likes", "interested_in"}},
	{"Sophie has cat Mochi", "sophie", "mochi", []string{"owns", "has"}},
	{"Sophie partner Tom", "sophie", "tom", []string{"partner_of", "married_to", "knows"}},
	{"Tom works at Figma", "tom", "figma", []string{"works_at"}},
	{"Sophie sister Emma", "sophie", "emma", []string{"sibling_of", "knows", "related_to"}},
	{"Emma lives in Tokyo/teaches in Shibuya", "emma", "shibuya", []string{"lives_in", "teaches_at", "located_in"}},
	{"Sophie speaks/learns Japanese", "sophie", "japanese", []string{"speaks", "interested_in", "likes", "reads", "writes"}},
	{"Sophie uses/likes Python", "sophie", "python", []string{"uses", "knows", "likes"}},
	{"Sophie uses/likes Rust", "sophie", "rust", []string{"uses", "knows", "likes"}},
	{"Sophie allergic peanuts", "sophie", "peanut", []string{"allergic_to", "dislikes"}},
}

var negativeFacts = []negFact{
	{"assistant opinions", []string{"amazing", "incredible", "great trajectory"}},
	{"meta commentary", []string{"told me", "mentioned", "shared"}},
	{"assistant name", []string{"assistant", "AI", "chatbot"}},
}

// --- Update scenario: Sophie moves and changes jobs ---

var updateConversation = []Message{
	{Role: "user", Content: "Hey, big update! I left DeepMind last month. I just started as a research lead at Anthropic in San Francisco. Tom and I moved here together."},
	{Role: "assistant", Content: "Wow, congratulations! That's a huge move!"},
	{Role: "user", Content: "Yeah, I'm excited! We found a place in the Mission District. Oh and I switched from Arch to NixOS — much better for reproducibility."},
}

type updateCheck struct {
	label       string
	mustFind    []string // at least one must appear
	mustNotFind []string // none should appear (superseded)
}

var updateChecks = []updateCheck{
	{"works at Anthropic now", []string{"Anthropic"}, []string{}},
	{"no longer ML engineer at DeepMind", []string{}, []string{"engineer at DeepMind", "works at DeepMind"}},
	{"lives in SF now", []string{"San Francisco", "Mission District"}, []string{}},
	{"uses NixOS now", []string{"NixOS", "Nix"}, []string{}},
	{"Arch Linux should be gone", []string{}, []string{"Arch Linux"}},
}

// --- Test harness ---

func TestQuality_FactRecall(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Logf("Extraction: added=%d updated=%d tokens=%d", len(result.Added), len(result.Updated), result.Tokens)

	facts, err := mem.GetAll(ctx, "sophie", 200)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== STORED FACTS (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	// Score recall
	var hits, misses int
	for _, ef := range expectedFacts {
		found := matchFact(facts, ef.matchAny)
		if found != "" {
			hits++
		} else {
			misses++
			t.Logf("MISS: %s (searched for any of: %v)", ef.label, ef.matchAny)
		}
	}

	recall := float64(hits) / float64(hits+misses) * 100
	t.Logf("\n=== FACT RECALL: %.0f%% (%d/%d) ===", recall, hits, hits+misses)
	if recall < 70 {
		t.Errorf("fact recall %.0f%% is below 70%% threshold", recall)
	}
}

func TestQuality_FactPrecision(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	_, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	facts, err := mem.GetAll(ctx, "sophie", 200)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	// Check for false positives
	var falsePositives int
	for _, nf := range negativeFacts {
		for _, f := range facts {
			if matchesAll(f.Text, nf.matchAll) {
				falsePositives++
				t.Errorf("FALSE POSITIVE: %s — found: %q", nf.label, f.Text)
			}
		}
	}
	t.Logf("False positives: %d", falsePositives)
}

func TestQuality_RelationRecall(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	_, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	rels, err := mem.GetAllRelations(ctx, "sophie", 200)
	if err != nil {
		t.Fatalf("GetAllRelations: %v", err)
	}

	t.Logf("\n=== STORED RELATIONS (%d) ===", len(rels))
	for i, r := range rels {
		t.Logf("  %2d. %s --%s--> %s", i+1, r.Source, r.Relation, r.Target)
	}

	// Check relation type consistency — all must be from our enum
	allowedRelations := map[string]bool{
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
		if !allowedRelations[r.Relation] {
			t.Errorf("INVALID RELATION TYPE: %q in %s --%s--> %s", r.Relation, r.Source, r.Relation, r.Target)
		}
	}

	// Score relation recall
	var hits, misses int
	for _, er := range expectedRelations {
		found := matchRelation(rels, er)
		if found {
			hits++
		} else {
			misses++
			t.Logf("MISS: %s (source~%q target~%q relation in %v)", er.label, er.source, er.target, er.relation)
		}
	}

	recall := float64(hits) / float64(hits+misses) * 100
	t.Logf("\n=== RELATION RECALL: %.0f%% (%d/%d) ===", recall, hits, hits+misses)
	if recall < 50 {
		t.Errorf("relation recall %.0f%% is below 50%% threshold", recall)
	}
}

func TestQuality_SearchRelevance(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	_, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	queries := []struct {
		query    string
		wantAny  []string // at least one result must contain one of these
		minScore float64
	}{
		{"where does Sophie live?", []string{"London", "Shoreditch", "london", "shoreditch"}, 0.4},
		{"what is Sophie's job?", []string{"engineer", "ML", "machine learning", "DeepMind"}, 0.4},
		{"Sophie's pets", []string{"cat", "Mochi", "mochi"}, 0.4},
		{"food allergies", []string{"peanut", "allergic"}, 0.3},
		{"programming languages", []string{"Python", "Rust", "python", "rust"}, 0.3},
		{"Sophie's family", []string{"Emma", "sister", "Tom", "boyfriend"}, 0.3},
	}

	for _, q := range queries {
		results, err := mem.Search(ctx, q.query, "sophie", 5)
		if err != nil {
			t.Errorf("Search %q: %v", q.query, err)
			continue
		}

		t.Logf("\nQuery: %q", q.query)
		for _, f := range results {
			t.Logf("  [%.2f] %s", f.Score, f.Text)
		}

		found := false
		for _, f := range results {
			if f.Score >= q.minScore {
				for _, want := range q.wantAny {
					if containsCI(f.Text, want) {
						found = true
						break
					}
				}
			}
			if found {
				break
			}
		}
		if !found {
			t.Logf("SEARCH MISS: %q — no result with score >= %.1f containing %v", q.query, q.minScore, q.wantAny)
		}
	}
}

func TestQuality_UpdateReconciliation(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	// First round: initial facts
	_, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add initial: %v", err)
	}

	// Second round: updates
	r2, err := mem.Add(ctx, updateConversation, "sophie")
	if err != nil {
		t.Fatalf("Add update: %v", err)
	}
	t.Logf("Update round: added=%d updated=%d deleted=%d noops=%d",
		len(r2.Added), len(r2.Updated), len(r2.Deleted), r2.Noops)

	facts, err := mem.GetAll(ctx, "sophie", 200)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== FACTS AFTER UPDATE (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	for _, uc := range updateChecks {
		if len(uc.mustFind) > 0 {
			found := false
			for _, f := range facts {
				for _, want := range uc.mustFind {
					if containsCI(f.Text, want) {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				t.Errorf("UPDATE MISS: %s — expected one of %v in facts", uc.label, uc.mustFind)
			}
		}

		for _, bad := range uc.mustNotFind {
			for _, f := range facts {
				if containsCI(f.Text, bad) {
					t.Errorf("STALE FACT: %s — %q should have been updated/removed, found in: %q", uc.label, bad, f.Text)
				}
			}
		}
	}
}

// --- Helpers ---

func matchFact(facts []Fact, anySubstr []string) string {
	for _, f := range facts {
		for _, sub := range anySubstr {
			if containsCI(f.Text, sub) {
				return f.Text
			}
		}
	}
	return ""
}

func matchRelation(rels []Relation, er expectedRelation) bool {
	for _, r := range rels {
		sourceMatch := containsCI(r.Source, er.source)
		targetMatch := containsCI(r.Target, er.target)
		relMatch := false
		for _, rel := range er.relation {
			if strings.EqualFold(r.Relation, rel) {
				relMatch = true
				break
			}
		}
		if sourceMatch && targetMatch && relMatch {
			return true
		}
		// Also try reversed direction (source/target can be swapped)
		if containsCI(r.Source, er.target) && containsCI(r.Target, er.source) && relMatch {
			return true
		}
	}
	return false
}

func matchesAll(text string, substrs []string) bool {
	for _, s := range substrs {
		if !containsCI(text, s) {
			return false
		}
	}
	return true
}

func containsCI(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// TestQuality_Report runs all quality checks and prints a summary report.
func TestQuality_Report(t *testing.T) {
	skipIfNoKey(t)

	db := filepath.Join(t.TempDir(), "test.db")
	mem, err := New(Config{
		DBPath:    db,
		LLMAPIKey: os.Getenv("OPENAI_API_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()

	ctx := context.Background()

	result, err := mem.Add(ctx, complexConversation, "sophie")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	facts, _ := mem.GetAll(ctx, "sophie", 200)
	rels, _ := mem.GetAllRelations(ctx, "sophie", 200)

	// Fact recall
	factHits := 0
	var factMisses []string
	for _, ef := range expectedFacts {
		if matchFact(facts, ef.matchAny) != "" {
			factHits++
		} else {
			factMisses = append(factMisses, ef.label)
		}
	}
	factRecall := float64(factHits) / float64(len(expectedFacts)) * 100

	// Relation recall
	relHits := 0
	var relMisses []string
	for _, er := range expectedRelations {
		if matchRelation(rels, er) {
			relHits++
		} else {
			relMisses = append(relMisses, er.label)
		}
	}
	relRecall := float64(relHits) / float64(len(expectedRelations)) * 100

	// False positives
	falsePos := 0
	for _, nf := range negativeFacts {
		for _, f := range facts {
			if matchesAll(f.Text, nf.matchAll) {
				falsePos++
			}
		}
	}

	// Relation type violations
	allowedRelations := map[string]bool{
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

	var invalidRelTypes []string
	for _, r := range rels {
		if !allowedRelations[r.Relation] {
			invalidRelTypes = append(invalidRelTypes, fmt.Sprintf("%s --%s--> %s", r.Source, r.Relation, r.Target))
		}
	}

	// Print report
	t.Logf("\n%s", strings.Repeat("=", 60))
	t.Logf("  QUALITY REPORT")
	t.Logf("%s", strings.Repeat("=", 60))
	t.Logf("")
	t.Logf("  Facts extracted:     %d", len(facts))
	t.Logf("  Relations extracted: %d", len(rels))
	t.Logf("  Tokens used:         %d", result.Tokens)
	t.Logf("")
	t.Logf("  FACT RECALL:         %.0f%% (%d/%d)", factRecall, factHits, len(expectedFacts))
	if len(factMisses) > 0 {
		t.Logf("    Misses:")
		for _, m := range factMisses {
			t.Logf("      - %s", m)
		}
	}
	t.Logf("")
	t.Logf("  RELATION RECALL:     %.0f%% (%d/%d)", relRecall, relHits, len(expectedRelations))
	if len(relMisses) > 0 {
		t.Logf("    Misses:")
		for _, m := range relMisses {
			t.Logf("      - %s", m)
		}
	}
	t.Logf("")
	t.Logf("  FALSE POSITIVES:     %d", falsePos)
	t.Logf("  INVALID REL TYPES:   %d", len(invalidRelTypes))
	for _, v := range invalidRelTypes {
		t.Logf("    - %s", v)
	}
	t.Logf("")

	grade := "A"
	switch {
	case factRecall < 50 || relRecall < 30:
		grade = "F"
	case factRecall < 60 || relRecall < 40:
		grade = "D"
	case factRecall < 70 || relRecall < 50:
		grade = "C"
	case factRecall < 80 || relRecall < 60:
		grade = "B"
	}
	if falsePos > 0 {
		grade = string(min(grade[0]+1, 'F'))
	}
	if len(invalidRelTypes) > 0 {
		grade = string(min(grade[0]+1, 'F'))
	}

	t.Logf("  GRADE:               %s", grade)
	t.Logf("%s", strings.Repeat("=", 60))

	if grade > "C" {
		t.Errorf("quality grade %s is below acceptable threshold (C)", grade)
	}
}

//go:build integration

package faktory

import (
	"context"
	"strings"
	"testing"
)

// --- Scenario 1: Multi-language (English + French mix) ---

var multiLangConversation = []Message{
	{Role: "user", Content: "Salut ! Je m'appelle Marc. I work as a chef in Paris."},
	{Role: "assistant", Content: "Hi Marc! A chef in Paris, that's wonderful!"},
	{Role: "user", Content: "Oui, je travaille au restaurant Le Cinq depuis 5 ans. My specialty is French-Japanese fusion."},
	{Role: "assistant", Content: "Interesting fusion! Do you have other interests?"},
	{Role: "user", Content: "J'adore le vélo, I cycle to work every day. Et je parle trois langues: français, anglais, et japonais."},
}

var multiLangExpected = []expectedFact{
	{"name is Marc", []string{"Marc", "marc"}},
	{"chef", []string{"chef"}},
	{"works in Paris", []string{"Paris", "paris"}},
	{"Le Cinq restaurant", []string{"Le Cinq", "Cinq"}},
	{"5 years", []string{"5 year", "five year"}},
	{"French-Japanese fusion", []string{"fusion", "Japanese", "French"}},
	{"cycling", []string{"cycl", "vélo", "bike"}},
	{"speaks 3 languages", []string{"français", "french", "anglais", "english", "japonais", "japanese", "three lang", "3 lang"}},
}

func TestAdversarial_MultiLanguage(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, multiLangConversation, "marc")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Logf("Added=%d Updated=%d Tokens=%d", len(result.Added), len(result.Updated), result.Tokens)

	facts, err := mem.GetAll(ctx, "marc", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== STORED FACTS (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	var hits int
	for _, ef := range multiLangExpected {
		if matchFact(facts, ef.matchAny) != "" {
			hits++
		} else {
			t.Logf("MISS: %s (any of: %v)", ef.label, ef.matchAny)
		}
	}
	recall := float64(hits) / float64(len(multiLangExpected)) * 100
	t.Logf("\n=== MULTI-LANG RECALL: %.0f%% (%d/%d) ===", recall, hits, len(multiLangExpected))
	if recall < 50 {
		t.Errorf("multi-language recall %.0f%% below 50%% threshold", recall)
	}
}

// --- Scenario 2: Sarcasm and negation ---

var sarcasmConversation = []Message{
	{Role: "user", Content: "I'm Lisa. I absolutely LOVE waking up at 5am. Just kidding, I hate mornings."},
	{Role: "assistant", Content: "Ha! Not a morning person then?"},
	{Role: "user", Content: "Definitely not. I'm a night owl. I don't like coffee at all, I drink tea exclusively. And despite what people think, I'm NOT a fan of sushi — I'm allergic to fish."},
	{Role: "assistant", Content: "Tea person with a fish allergy, got it!"},
	{Role: "user", Content: "Yeah. I used to like running but I tore my ACL last year, so now I do swimming instead."},
}

var sarcasmExpected = []expectedFact{
	{"name Lisa", []string{"Lisa", "lisa"}},
	{"hates mornings / not a morning person", []string{"morning", "hate", "night owl"}},
	{"night owl", []string{"night owl", "night"}},
	{"doesn't like coffee", []string{"coffee", "tea"}},
	{"drinks tea", []string{"tea"}},
	{"not a fan of sushi / allergic to fish", []string{"fish", "sushi", "allergic"}},
	{"tore ACL", []string{"ACL", "acl", "knee", "injur"}},
	{"does swimming now", []string{"swim"}},
}

var sarcasmNegatives = []negFact{
	{"should NOT think she loves 5am", []string{"loves", "5am"}},
	{"should NOT think she likes coffee", []string{"likes coffee", "enjoys coffee", "drinks coffee"}},
	{"should NOT think she likes sushi", []string{"likes sushi", "enjoys sushi", "fan of sushi"}},
}

func TestAdversarial_SarcasmNegation(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, sarcasmConversation, "lisa")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Logf("Added=%d Updated=%d Tokens=%d", len(result.Added), len(result.Updated), result.Tokens)

	facts, err := mem.GetAll(ctx, "lisa", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== STORED FACTS (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	// Recall
	var hits int
	for _, ef := range sarcasmExpected {
		if matchFact(facts, ef.matchAny) != "" {
			hits++
		} else {
			t.Logf("MISS: %s (any of: %v)", ef.label, ef.matchAny)
		}
	}
	recall := float64(hits) / float64(len(sarcasmExpected)) * 100
	t.Logf("\n=== SARCASM RECALL: %.0f%% (%d/%d) ===", recall, hits, len(sarcasmExpected))
	if recall < 50 {
		t.Errorf("sarcasm recall %.0f%% below 50%% threshold", recall)
	}

	// False positives — sarcasm misread
	for _, nf := range sarcasmNegatives {
		for _, f := range facts {
			if matchesAll(f.Text, nf.matchAll) {
				t.Errorf("SARCASM FALSE POSITIVE: %s — found: %q", nf.label, f.Text)
			}
		}
	}
}

// --- Scenario 3: Self-correction within same conversation ---

var selfCorrectionConversation = []Message{
	{Role: "user", Content: "I'm Jake. I live in Berlin."},
	{Role: "assistant", Content: "Nice, Berlin is great!"},
	{Role: "user", Content: "Actually wait, I moved last month. I live in Munich now, sorry about that."},
	{Role: "assistant", Content: "No worries! Munich is lovely too."},
	{Role: "user", Content: "I work at BMW. Oh wait no, I meant Siemens — I always mix them up. I'm a mechanical engineer there."},
	{Role: "assistant", Content: "Siemens, got it! Mechanical engineering sounds interesting."},
	{Role: "user", Content: "Yeah. I have 2 kids. Well actually, 3 — I always forget to count the baby haha. The oldest is 7."},
}

var selfCorrectionMustFind = []expectedFact{
	{"lives in Munich (corrected)", []string{"Munich", "munich"}},
	{"works at Siemens (corrected)", []string{"Siemens", "siemens"}},
	{"mechanical engineer", []string{"mechanical engineer"}},
	{"3 kids (corrected)", []string{"3 kid", "three kid", "3 child", "three child"}},
	{"oldest is 7", []string{"7", "oldest", "seven"}},
}

var selfCorrectionMustNotFind = []negFact{
	{"should NOT store Berlin as current city", []string{"lives in Berlin", "live in Berlin"}},
	{"should NOT store BMW as employer", []string{"works at BMW", "work at BMW"}},
	{"should NOT store 2 kids", []string{"2 kid", "two kid", "2 child", "two child"}},
}

func TestAdversarial_SelfCorrection(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, selfCorrectionConversation, "jake")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Logf("Added=%d Updated=%d Tokens=%d", len(result.Added), len(result.Updated), result.Tokens)

	facts, err := mem.GetAll(ctx, "jake", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== STORED FACTS (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	// Must find corrected facts
	var hits int
	for _, ef := range selfCorrectionMustFind {
		if matchFact(facts, ef.matchAny) != "" {
			hits++
		} else {
			t.Logf("MISS: %s (any of: %v)", ef.label, ef.matchAny)
		}
	}
	recall := float64(hits) / float64(len(selfCorrectionMustFind)) * 100
	t.Logf("\n=== SELF-CORRECTION RECALL: %.0f%% (%d/%d) ===", recall, hits, len(selfCorrectionMustFind))
	if recall < 40 {
		t.Errorf("self-correction recall %.0f%% below 40%% threshold", recall)
	}

	// Must NOT find pre-correction facts
	for _, nf := range selfCorrectionMustNotFind {
		for _, f := range facts {
			if matchesAll(f.Text, nf.matchAll) {
				t.Errorf("STALE (pre-correction): %s — found: %q", nf.label, f.Text)
			}
		}
	}
}

// --- Scenario 4: Long conversation (20+ turns) ---

var longConversation = []Message{
	{Role: "user", Content: "Hi, I'm Nina!"},
	{Role: "assistant", Content: "Hi Nina!"},
	{Role: "user", Content: "I'm a 35-year-old architect."},
	{Role: "assistant", Content: "Cool! Where do you work?"},
	{Role: "user", Content: "I work at Foster + Partners in London."},
	{Role: "assistant", Content: "That's a prestigious firm!"},
	{Role: "user", Content: "I specialize in sustainable building design."},
	{Role: "assistant", Content: "Important work. Any notable projects?"},
	{Role: "user", Content: "I'm currently working on a net-zero office tower in Dubai."},
	{Role: "assistant", Content: "Impressive! How about your personal life?"},
	{Role: "user", Content: "I'm married to Alex, who's a photographer."},
	{Role: "assistant", Content: "Nice! Do you have any hobbies?"},
	{Role: "user", Content: "I paint watercolors on weekends. I also practice yoga every morning."},
	{Role: "assistant", Content: "Creative and healthy! What about food?"},
	{Role: "user", Content: "I'm vegan. My favorite cuisine is Ethiopian."},
	{Role: "assistant", Content: "Ethiopian food is amazing! Travel?"},
	{Role: "user", Content: "I've visited 40 countries. My favorite was Japan."},
	{Role: "assistant", Content: "Wow, 40 countries! Any upcoming trips?"},
	{Role: "user", Content: "Going to Patagonia in December for a hiking trip."},
	{Role: "assistant", Content: "That sounds incredible!"},
	{Role: "user", Content: "I also have a golden retriever named Luna."},
	{Role: "assistant", Content: "Dogs are the best!"},
	{Role: "user", Content: "She's 4 years old. I adopted her from a shelter."},
	{Role: "assistant", Content: "Adoption is wonderful. Anything else?"},
	{Role: "user", Content: "I'm learning to play piano. Started last year. And I volunteer at Habitat for Humanity on weekends."},
}

var longConvoExpected = []expectedFact{
	{"name Nina", []string{"Nina", "nina"}},
	{"35 years old", []string{"35"}},
	{"architect", []string{"architect"}},
	{"Foster + Partners", []string{"Foster", "Partners"}},
	{"London", []string{"London", "london"}},
	{"sustainable design", []string{"sustainable", "net-zero", "green"}},
	{"Dubai project", []string{"Dubai", "dubai"}},
	{"married to Alex", []string{"Alex", "alex", "married", "spouse", "partner"}},
	{"Alex is photographer", []string{"photographer"}},
	{"watercolors", []string{"watercolor", "paint"}},
	{"yoga", []string{"yoga"}},
	{"vegan", []string{"vegan"}},
	{"Ethiopian cuisine", []string{"Ethiopian", "ethiopian"}},
	{"40 countries", []string{"40 countr"}},
	{"favorite Japan", []string{"Japan", "japan"}},
	{"Patagonia trip", []string{"Patagonia", "patagonia"}},
	{"golden retriever Luna", []string{"Luna", "luna", "golden retriever"}},
	{"piano", []string{"piano"}},
	{"Habitat for Humanity", []string{"Habitat", "habitat", "volunteer"}},
}

func TestAdversarial_LongConversation(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	result, err := mem.Add(ctx, longConversation, "nina")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Logf("Added=%d Updated=%d Tokens=%d", len(result.Added), len(result.Updated), result.Tokens)

	facts, err := mem.GetAll(ctx, "nina", 200)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("\n=== STORED FACTS (%d) ===", len(facts))
	for i, f := range facts {
		t.Logf("  %2d. %s", i+1, f.Text)
	}

	var hits int
	for _, ef := range longConvoExpected {
		if matchFact(facts, ef.matchAny) != "" {
			hits++
		} else {
			t.Logf("MISS: %s (any of: %v)", ef.label, ef.matchAny)
		}
	}
	recall := float64(hits) / float64(len(longConvoExpected)) * 100
	t.Logf("\n=== LONG CONVO RECALL: %.0f%% (%d/%d) ===", recall, hits, len(longConvoExpected))
	if recall < 60 {
		t.Errorf("long conversation recall %.0f%% below 60%% threshold", recall)
	}
}

// --- Scenario 5: Minimal / trivial conversation ---

func TestAdversarial_MinimalConversation(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	// "hi" should extract zero meaningful facts
	result, err := mem.Add(ctx, []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "Hello! How can I help you?"},
		{Role: "user", Content: "nothing, just saying hi"},
	}, "anon")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	facts, err := mem.GetAll(ctx, "anon", 100)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	t.Logf("Added=%d, stored=%d, tokens=%d", len(result.Added), len(facts), result.Tokens)
	for _, f := range facts {
		t.Logf("  fact: %s", f.Text)
	}

	if len(facts) > 1 {
		t.Errorf("minimal conversation stored %d facts, want 0-1 (greetings have no memorable content)", len(facts))
	}
}

// --- Scenario 6: Adversarial report ---

func TestAdversarial_Report(t *testing.T) {
	skipIfNoKey(t)

	mem := newTestMemory(t)
	ctx := context.Background()

	type scenario struct {
		name     string
		msgs     []Message
		userID   string
		expected []expectedFact
	}

	scenarios := []scenario{
		{"multi-language", multiLangConversation, "marc", multiLangExpected},
		{"sarcasm", sarcasmConversation, "lisa", sarcasmExpected},
		{"self-correction", selfCorrectionConversation, "jake", selfCorrectionMustFind},
		{"long-convo", longConversation, "nina", longConvoExpected},
	}

	t.Logf("\n%s", strings.Repeat("=", 60))
	t.Logf("  ADVERSARIAL QUALITY REPORT")
	t.Logf("%s", strings.Repeat("=", 60))

	totalHits, totalExpected := 0, 0
	for _, s := range scenarios {
		result, err := mem.Add(ctx, s.msgs, s.userID)
		if err != nil {
			t.Errorf("%s: Add failed: %v", s.name, err)
			continue
		}

		facts, _ := mem.GetAll(ctx, s.userID, 200)
		var hits int
		for _, ef := range s.expected {
			if matchFact(facts, ef.matchAny) != "" {
				hits++
			}
		}
		recall := float64(hits) / float64(len(s.expected)) * 100
		t.Logf("  %-20s recall=%.0f%% (%d/%d) added=%d tokens=%d",
			s.name, recall, hits, len(s.expected), len(result.Added), result.Tokens)
		totalHits += hits
		totalExpected += len(s.expected)
	}

	overall := float64(totalHits) / float64(totalExpected) * 100
	t.Logf("")
	t.Logf("  OVERALL:             %.0f%% (%d/%d)", overall, totalHits, totalExpected)
	t.Logf("%s", strings.Repeat("=", 60))

	if overall < 50 {
		t.Errorf("overall adversarial recall %.0f%% below 50%% threshold", overall)
	}
}

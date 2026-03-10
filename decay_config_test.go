package faktory

import (
	"math"
	"testing"
	"time"
)

func makeFacts(ageDays float64, accessCount int) []Fact {
	created := time.Now().Add(-time.Duration(ageDays*24) * time.Hour).Format(time.RFC3339)
	return []Fact{{
		ID:          "f1",
		Text:        "test fact",
		Score:       1.0,
		CreatedAt:   created,
		AccessCount: accessCount,
	}}
}

func TestDecayCustomAlpha(t *testing.T) {
	facts := makeFacts(100, 0)

	defaultAlpha := 0.01
	customAlpha := 0.1 // 10x faster decay
	defaultBeta := 0.1

	// Score with default alpha
	d := make([]Fact, len(facts))
	copy(d, facts)
	applyDecay(d, defaultAlpha, defaultBeta)
	defaultScore := d[0].Score

	// Score with custom (higher) alpha
	c := make([]Fact, len(facts))
	copy(c, facts)
	applyDecay(c, customAlpha, defaultBeta)
	customScore := c[0].Score

	if customScore >= defaultScore {
		t.Errorf("higher alpha should produce lower score: default=%.6f custom=%.6f", defaultScore, customScore)
	}

	// Verify the scores are meaningfully different (not just floating-point noise)
	diff := math.Abs(defaultScore - customScore)
	if diff < 0.01 {
		t.Errorf("scores should differ meaningfully: diff=%.6f", diff)
	}
}

func TestDecayCustomBeta(t *testing.T) {
	facts := makeFacts(10, 5)

	defaultAlpha := 0.01
	defaultBeta := 0.1
	customBeta := 1.0 // 10x stronger access boost

	// Score with default beta
	d := make([]Fact, len(facts))
	copy(d, facts)
	applyDecay(d, defaultAlpha, defaultBeta)
	defaultScore := d[0].Score

	// Score with custom (higher) beta
	c := make([]Fact, len(facts))
	copy(c, facts)
	applyDecay(c, defaultAlpha, customBeta)
	customScore := c[0].Score

	if customScore <= defaultScore {
		t.Errorf("higher beta should produce higher score: default=%.6f custom=%.6f", defaultScore, customScore)
	}

	diff := math.Abs(customScore - defaultScore)
	if diff < 0.01 {
		t.Errorf("scores should differ meaningfully: diff=%.6f", diff)
	}
}

func TestDecayDefaultsWhenZero(t *testing.T) {
	cfg := Config{}.withDefaults()

	if cfg.DecayAlpha != 0.01 {
		t.Errorf("DecayAlpha default = %f, want 0.01", cfg.DecayAlpha)
	}
	if cfg.DecayBeta != 0.1 {
		t.Errorf("DecayBeta default = %f, want 0.1", cfg.DecayBeta)
	}

	// Explicit values should not be overwritten
	cfg2 := Config{DecayAlpha: 0.05, DecayBeta: 0.5}.withDefaults()
	if cfg2.DecayAlpha != 0.05 {
		t.Errorf("custom DecayAlpha overwritten: %f", cfg2.DecayAlpha)
	}
	if cfg2.DecayBeta != 0.5 {
		t.Errorf("custom DecayBeta overwritten: %f", cfg2.DecayBeta)
	}
}

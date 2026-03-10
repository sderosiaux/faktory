package faktory

import (
	"testing"
)

func TestHashFact(t *testing.T) {
	h1 := hashFact("likes pizza")
	h2 := hashFact("likes pizza")
	h3 := hashFact("likes pasta")

	if h1 != h2 {
		t.Error("same text should produce same hash")
	}
	if h1 == h3 {
		t.Error("different text should produce different hash")
	}
}

func TestFormatMessages(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	got := formatMessages(msgs)
	want := "user: Hello\nassistant: Hi there\n"
	if got != want {
		t.Errorf("formatMessages = %q, want %q", got, want)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}.withDefaults()

	if cfg.DBPath != "faktory.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LLMModel != "gpt-4o-mini" {
		t.Errorf("LLMModel = %q", cfg.LLMModel)
	}
	if cfg.EmbedDimension != 1536 {
		t.Errorf("EmbedDimension = %d", cfg.EmbedDimension)
	}

	// Custom values should not be overwritten
	cfg2 := Config{DBPath: "custom.db", EmbedDimension: 768}.withDefaults()
	if cfg2.DBPath != "custom.db" {
		t.Errorf("custom DBPath overwritten: %q", cfg2.DBPath)
	}
	if cfg2.EmbedDimension != 768 {
		t.Errorf("custom dimension overwritten: %d", cfg2.EmbedDimension)
	}
}

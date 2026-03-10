package faktory

import (
	"strings"
	"testing"
)

var testLog = nopLogger()

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

func TestTruncateMessages(t *testing.T) {
	t.Run("no-op when under limit", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
		}
		got := truncateMessages(testLog, msgs,1000)
		if len(got) != 2 {
			t.Errorf("expected 2 messages, got %d", len(got))
		}
	})

	t.Run("truncates to fit", func(t *testing.T) {
		msgs := make([]Message, 10)
		for i := range msgs {
			msgs[i] = Message{Role: "user", Content: strings.Repeat("x", 100)}
		}
		// Each message is ~107 chars ("user: " + 100 + "\n"). 3 messages ≈ 321 chars.
		got := truncateMessages(testLog, msgs,350)
		if len(got) > 3 {
			t.Errorf("expected <=3 messages, got %d", len(got))
		}
		if len(got) == 0 {
			t.Fatal("expected at least 1 message")
		}
	})

	t.Run("keeps at least one message", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: strings.Repeat("x", 1000)},
		}
		got := truncateMessages(testLog, msgs,10)
		if len(got) != 1 {
			t.Errorf("expected 1 message, got %d", len(got))
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "first"},
			{Role: "user", Content: "second"},
			{Role: "user", Content: "third"},
		}
		got := truncateMessages(testLog, msgs,50)
		if len(got) > 0 && got[len(got)-1].Content != "third" {
			t.Errorf("last message should be 'third', got %q", got[len(got)-1].Content)
		}
	})
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

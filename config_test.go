package faktory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromTOML(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "faktory.toml")
	os.WriteFile(tomlPath, []byte(`
db_path = "test.db"
llm_model = "gpt-4o"
embed_dimension = 768
`), 0644)

	// Change to temp dir so LoadConfig finds faktory.toml
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	// Clear env vars that would override
	for _, k := range []string{"FAKTORY_DB", "FAKTORY_MODEL", "FAKTORY_EMBED_DIM"} {
		t.Setenv(k, "")
	}

	cfg := LoadConfig()
	if cfg.DBPath != "test.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "test.db")
	}
	if cfg.LLMModel != "gpt-4o" {
		t.Errorf("LLMModel = %q, want %q", cfg.LLMModel, "gpt-4o")
	}
	if cfg.EmbedDimension != 768 {
		t.Errorf("EmbedDimension = %d, want 768", cfg.EmbedDimension)
	}
}

func TestLoadConfigEnvOverridesToml(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "faktory.toml")
	os.WriteFile(tomlPath, []byte(`
db_path = "toml.db"
llm_model = "gpt-4o"
`), 0644)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	t.Setenv("FAKTORY_DB", "env.db")
	t.Setenv("FAKTORY_MODEL", "")

	cfg := LoadConfig()
	if cfg.DBPath != "env.db" {
		t.Errorf("env should override TOML: DBPath = %q, want %q", cfg.DBPath, "env.db")
	}
	if cfg.LLMModel != "gpt-4o" {
		t.Errorf("LLMModel = %q, want %q (from TOML, env empty)", cfg.LLMModel, "gpt-4o")
	}
}

func TestLoadConfigPartialTOML(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "faktory.toml")
	os.WriteFile(tomlPath, []byte(`llm_model = "custom-model"`), 0644)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	for _, k := range []string{"FAKTORY_DB", "FAKTORY_BASE_URL", "FAKTORY_API_KEY", "FAKTORY_MODEL", "FAKTORY_EMBED_MODEL", "FAKTORY_EMBED_DIM"} {
		t.Setenv(k, "")
	}

	cfg := LoadConfig()
	if cfg.LLMModel != "custom-model" {
		t.Errorf("LLMModel = %q, want %q", cfg.LLMModel, "custom-model")
	}
	// Unset fields should be zero-values (withDefaults fills them later)
	if cfg.DBPath != "" {
		t.Errorf("DBPath should be empty before withDefaults, got %q", cfg.DBPath)
	}
}

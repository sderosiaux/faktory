package faktory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type tomlConfig struct {
	DBPath         string `toml:"db_path"`
	LLMBaseURL     string `toml:"llm_base_url"`
	LLMAPIKey      string `toml:"llm_api_key"`
	LLMModel       string `toml:"llm_model"`
	EmbedModel     string `toml:"embed_model"`
	EmbedDimension int    `toml:"embed_dimension"`
}

// LoadConfig builds a Config by layering: code defaults < TOML file < env vars.
func LoadConfig() Config {
	var tc tomlConfig
	for _, path := range configPaths() {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, &tc); err != nil {
				log.Printf("warning: failed to parse %s: %v", path, err)
			}
			break
		}
	}

	cfg := Config(tc)

	// Env vars override TOML
	if v := os.Getenv("FAKTORY_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("FAKTORY_BASE_URL"); v != "" {
		cfg.LLMBaseURL = v
	}
	if v := os.Getenv("FAKTORY_API_KEY"); v != "" {
		cfg.LLMAPIKey = v
	}
	if v := os.Getenv("FAKTORY_MODEL"); v != "" {
		cfg.LLMModel = v
	}
	if v := os.Getenv("FAKTORY_EMBED_MODEL"); v != "" {
		cfg.EmbedModel = v
	}
	if v := os.Getenv("FAKTORY_EMBED_DIM"); v != "" {
		var dim int
		if _, err := fmt.Sscanf(v, "%d", &dim); err == nil && dim > 0 {
			cfg.EmbedDimension = dim
		}
	}

	return cfg
}

func configPaths() []string {
	paths := []string{"faktory.toml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "faktory", "faktory.toml"))
	}
	return paths
}

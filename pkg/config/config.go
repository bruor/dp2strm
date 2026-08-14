package config

import (
	"fmt"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
	"gopkg.in/yaml.v3"
)

// DefaultConfig returns safe default configuration parameters.
func DefaultConfig() *models.Config {
	return &models.Config{
		DispatcharrURL:        "http://localhost:8000",
		APIKey:                "",
		OutputDir:             "./strm_output",
		RetentionGraceDays:    7,
		ProxyPort:             8080,
		ProxyPublicURL:        "http://localhost:8080",
		SyncInterval:          0, // 0 = run once by default
		BatchSize:             1000,
		CircuitBreakerEnabled: true,
		MaxProbeConcurrency:   5,
	}
}

// LoadConfig reads configuration from a YAML file.
func LoadConfig(path string) (*models.Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		path = "config.yaml"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	// Environment variable overrides
	if envURL := os.Getenv("DISPATCHARR_URL"); envURL != "" {
		cfg.DispatcharrURL = envURL
	}
	if envKey := os.Getenv("DISPATCHARR_API_KEY"); envKey != "" {
		cfg.APIKey = envKey
	}
	if envOut := os.Getenv("OUTPUT_DIR"); envOut != "" {
		cfg.OutputDir = envOut
	}

	// Ensure output directory is absolute
	absDir, err := filepath.Abs(cfg.OutputDir)
	if err == nil {
		cfg.OutputDir = absDir
	}

	return cfg, nil
}

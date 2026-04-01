package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/goccy/go-yaml"
)

// Load reads a config file, detects format by extension, parses, applies defaults, and validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg, err := Parse(data, detectFormat(path))
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	ApplyDefaults(cfg)

	if errs := Validate(cfg); len(errs) > 0 {
		var sb strings.Builder
		sb.WriteString("config validation failed:\n")
		for _, e := range errs {
			sb.WriteString("  - ")
			sb.WriteString(e.Error())
			sb.WriteString("\n")
		}
		return nil, fmt.Errorf("%s", sb.String())
	}

	return cfg, nil
}

// Parse decodes raw bytes into Config using the specified format. Does not apply defaults or validate.
func Parse(data []byte, format string) (*Config, error) {
	var cfg Config

	switch format {
	case "yaml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("YAML parse error: %w", err)
		}
	case "toml":
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("TOML parse error: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format %q (use .yaml, .yml, or .toml)", format)
	}

	return &cfg, nil
}

func detectFormat(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	default:
		return ext
	}
}

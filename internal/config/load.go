package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

	data, err = expandEnvPlaceholders(data)
	if err != nil {
		return nil, fmt.Errorf("expanding environment placeholders: %w", err)
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
		return nil, fmt.Errorf("unsupported config format %q (use .yaml, .yml, .json, or .toml)", format)
	}

	return &cfg, nil
}

func detectFormat(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml", ".json":
		return "yaml"
	case ".toml":
		return "toml"
	default:
		return ext
	}
}

var envPlaceholderRE = regexp.MustCompile(`%\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnvPlaceholders(data []byte) ([]byte, error) {
	const escapedPrefixSentinel = "\x00pathosd-escaped-percent-lbrace\x00"

	input := strings.ReplaceAll(string(data), "%%{", escapedPrefixSentinel)
	missing := map[string]struct{}{}

	output := envPlaceholderRE.ReplaceAllStringFunc(input, func(match string) string {
		submatches := envPlaceholderRE.FindStringSubmatch(match)
		if len(submatches) != 2 {
			return match
		}

		envName := submatches[1]
		if value, ok := os.LookupEnv(envName); ok {
			return value
		}

		missing[envName] = struct{}{}
		return match
	})

	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		slices.Sort(names)
		return nil, fmt.Errorf("missing environment variables for placeholders: %s", strings.Join(names, ", "))
	}

	output = strings.ReplaceAll(output, escapedPrefixSentinel, "%{")
	return []byte(output), nil
}

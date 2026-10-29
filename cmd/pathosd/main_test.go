package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every subcommand that takes --config falls back to $PATHOSD_CONFIG, so a
// container or unit sets the path once for run, validate, check and
// healthcheck alike.
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("PATHOSD_CONFIG", "/etc/pathosd/pathosd.yaml")
	for _, args := range [][]string{{"validate"}, {"check"}, {"healthcheck"}, {"run"}} {
		t.Run(args[0], func(t *testing.T) {
			var cli CLI
			parser, err := kong.New(&cli)
			require.NoError(t, err)
			_, err = parser.Parse(args)
			if args[0] == "run" || args[0] == "validate" {
				// type:"existingfile" rejects the missing path, but only after
				// the env value was picked up.
				require.ErrorContains(t, err, "/etc/pathosd/pathosd.yaml")
				return
			}
			require.NoError(t, err)
			switch args[0] {
			case "check":
				assert.Equal(t, "/etc/pathosd/pathosd.yaml", cli.Check.Config)
			case "healthcheck":
				assert.Equal(t, "/etc/pathosd/pathosd.yaml", cli.Healthcheck.Config)
			}
		})
	}

	t.Run("flag wins over env", func(t *testing.T) {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse([]string{"check", "--config", "other.toml"})
		require.NoError(t, err)
		assert.Equal(t, "other.toml", cli.Check.Config)
	})
}

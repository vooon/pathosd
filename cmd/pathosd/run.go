package main

import (
	"fmt"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/daemon"
)

type RunCmd struct {
	Config string `help:"Path to configuration file." short:"c" type:"existingfile" required:"" env:"PATHOSD_CONFIG"`
	Debug  bool   `help:"Override configured log level and force debug logging."`
}

func applyRunOverrides(cfg *config.Config, debug bool) {
	if debug {
		cfg.Logging.Level = "debug"
	}
}

func (r *RunCmd) Run() error {
	cfg, raw, err := config.LoadRaw(r.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	applyRunOverrides(cfg, r.Debug)
	if err := daemon.Run(cfg, daemon.ConfigFile{Path: r.Config, Raw: raw, DebugForced: r.Debug}); err != nil {
		return fmt.Errorf("running daemon: %w", err)
	}
	return nil
}

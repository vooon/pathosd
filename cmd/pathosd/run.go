package main

import (
	"fmt"

	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/daemon"
)

type RunCmd struct{}

func (r *RunCmd) Run(cli *CLI) error {
	if cli.Config == "" {
		return fmt.Errorf("--config is required for the run command")
	}
	cfg, err := config.Load(cli.Config)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	daemon.Run(cfg)
	return nil
}

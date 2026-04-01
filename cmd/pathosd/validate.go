package main

import (
	"fmt"
	"os"

	"github.com/vooon/pathosd/internal/config"
)

type ValidateCmd struct{}

func (v *ValidateCmd) Run(cli *CLI) error {
	if cli.Config == "" {
		fmt.Fprintln(os.Stderr, "--config is required for the validate command")
		os.Exit(1)
	}
	cfg, err := config.Load(cli.Config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
		os.Exit(1)
	}

	_ = cfg
	fmt.Println("configuration is valid")
	return nil
}

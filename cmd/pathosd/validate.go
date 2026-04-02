package main

import (
	"fmt"

	"github.com/vooon/pathosd/internal/config"
)

type ValidateCmd struct {
	Config string `help:"Path to configuration file." short:"c" type:"existingfile" required:""`
}

func (v *ValidateCmd) Run() error {
	cfg, err := config.Load(v.Config)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	_ = cfg
	fmt.Println("configuration is valid")
	return nil
}

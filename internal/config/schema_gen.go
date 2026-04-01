//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/vooon/pathosd/internal/config"
)

func main() {
	r := new(jsonschema.Reflector)
	r.ExpandedStruct = true

	schema := r.Reflect(&config.Config{})
	schema.Title = "pathosd configuration"
	schema.Description = "Configuration schema for pathosd — health-aware BGP VIP announcer"
	schema.ID = "https://github.com/vooon/pathosd/schema/pathosd-config-v1.schema.json"

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling schema: %v\n", err)
		os.Exit(1)
	}

	// Ensure trailing newline
	data = append(data, '\n')

	outDir := "../../schema"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating schema dir: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outDir+"/pathosd-config-v1.schema.json", data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Generated schema/pathosd-config-v1.schema.json")
}

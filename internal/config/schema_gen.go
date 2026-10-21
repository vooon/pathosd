//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/invopop/jsonschema"
	"github.com/vooon/pathosd/internal/config"
)

func main() {
	r := new(jsonschema.Reflector)
	r.ExpandedStruct = true
	// Only fields tagged jsonschema:"required" are required; by default the
	// reflector would require every field that lacks omitempty, which rejects
	// any config that leaves an optional field out.
	r.RequiredFromJSONSchemaTags = true
	// config.Duration unmarshals from a Go duration string ("1s", "500ms");
	// without this mapping the reflector would describe it as an object.
	r.Mapper = func(t reflect.Type) *jsonschema.Schema {
		if t == reflect.TypeOf(config.Duration{}) {
			return &jsonschema.Schema{
				Type:        "string",
				Pattern:     `^([0-9]+(\.[0-9]*)?(ns|us|µs|ms|s|m|h))+$`,
				Description: "Duration string such as \"1s\", \"500ms\" or \"1m30s\".",
			}
		}
		return nil
	}

	if err := r.AddGoComments("github.com/vooon/pathosd/internal/config", "./"); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading Go comments: %v\n", err)
		os.Exit(1)
	}

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

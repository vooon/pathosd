package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/itchyny/gojq"
)

type JQTestCmd struct {
	Expression string `arg:"" help:"JQ expression to test."`
	File       string `help:"JSON file to read (default: stdin)." short:"f" type:"existingfile"`
}

func (cmd *JQTestCmd) Run() (err error) {
	query, err := gojq.Parse(cmd.Expression)
	if err != nil {
		return fmt.Errorf("invalid JQ expression: %w", err)
	}

	var input io.Reader = os.Stdin
	if cmd.File != "" {
		f, err := os.Open(cmd.File)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("closing %s: %w", cmd.File, closeErr)
			}
		}()
		input = f
	}

	var data interface{}
	if err := json.NewDecoder(input).Decode(&data); err != nil {
		return fmt.Errorf("invalid JSON input: %w", err)
	}

	iter := query.Run(data)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return fmt.Errorf("JQ error: %w", err)
		}
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
	}

	return nil
}

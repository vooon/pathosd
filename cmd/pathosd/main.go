package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/prometheus/common/version"
)

type CLI struct {
	Run      RunCmd           `cmd:"" default:"withargs" help:"Run the pathosd daemon."`
	Validate ValidateCmd      `cmd:"" help:"Validate configuration file."`
	Check    CheckCmd         `cmd:"" help:"Run the configured health checks once and print the results (no BGP)."`
	JQTest   JQTestCmd        `cmd:"" name:"jq-test" help:"Test a JQ expression against JSON input."`
	Version  kong.VersionFlag `help:"Print version and exit."`
}

func main() {
	cli := CLI{}
	ctx := kong.Parse(&cli,
		kong.Name("pathosd"),
		kong.Description("Health-aware BGP VIP announcer."),
		kong.UsageOnError(),
		kong.Vars{
			"version": fmt.Sprintf("pathosd %s", version.Info()),
		},
	)
	err := ctx.Run(&cli)
	var ec *exitCodeError
	if errors.As(err, &ec) {
		if ec.msg != "" {
			fmt.Fprintln(os.Stderr, ec.msg)
		}
		os.Exit(ec.code)
	}
	ctx.FatalIfErrorf(err)
}

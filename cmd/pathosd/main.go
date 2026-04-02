package main

import (
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/prometheus/common/version"
)

type CLI struct {
	Run      RunCmd           `cmd:"" default:"withargs" help:"Run the pathosd daemon."`
	Validate ValidateCmd      `cmd:"" help:"Validate configuration file."`
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
	ctx.FatalIfErrorf(err)
}

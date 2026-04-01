package main

import (
	"github.com/alecthomas/kong"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

type CLI struct {
	Config string `help:"Path to configuration file." short:"c" type:"existingfile"`

	Run      RunCmd      `cmd:"" default:"withargs" help:"Run the pathosd daemon."`
	Validate ValidateCmd `cmd:"" help:"Validate configuration file."`
	JQTest   JQTestCmd   `cmd:"" name:"jq-test" help:"Test a JQ expression against JSON input."`
	Version  VersionCmd  `cmd:"" help:"Print version and exit."`
}

type VersionCmd struct{}

func (v *VersionCmd) Run() error {
	println("pathosd " + version + " (" + commit + ") built " + date)
	return nil
}

func main() {
	cli := CLI{}
	ctx := kong.Parse(&cli,
		kong.Name("pathosd"),
		kong.Description("Health-aware BGP VIP announcer."),
		kong.UsageOnError(),
		kong.Vars{
			"version": version,
		},
	)
	err := ctx.Run(&cli)
	ctx.FatalIfErrorf(err)
}

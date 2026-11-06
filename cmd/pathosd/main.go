// Multipath TCP off for every listener and dialer in the binary. Go enables
// it for listeners by default since 1.24; MPTCP sockets reject TCP_MD5SIG,
// and a BGP speaker has no use for subflows. GoBGP already disables it on
// the BGP listener; this covers the HTTP API and the health checks too.
// GODEBUG=multipathtcp=... in the environment still overrides it.
//
//go:debug multipathtcp=0
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/prometheus/common/version"
)

type CLI struct {
	Run         RunCmd           `cmd:"" default:"withargs" help:"Run the pathosd daemon."`
	Validate    ValidateCmd      `cmd:"" help:"Validate configuration file."`
	Check       CheckCmd         `cmd:"" help:"Run the configured health checks once and print the results (no BGP)."`
	Healthcheck HealthcheckCmd   `cmd:"" help:"Ask a running daemon whether it is ready; container health check."`
	Reload      ReloadCmd        `cmd:"" help:"Make a running daemon re-read its config file and report whether it was applied."`
	JQTest      JQTestCmd        `cmd:"" name:"jq-test" help:"Test a JQ expression against JSON input."`
	Version     kong.VersionFlag `help:"Print version and exit."`
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

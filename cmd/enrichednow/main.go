// Command enrichednow is the command-line tool for the EnrichedNow API.
package main

import (
	"os"

	"github.com/enrichednow/enrichednow-cli/internal/cli"
)

// Set at build time with -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	os.Exit(cli.Main(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}

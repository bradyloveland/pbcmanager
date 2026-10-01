// Command pbcm-runner runs on each client. The server reaches it over SSH
// as the pbcm account, which can run nothing else.
package main

import (
	"os"

	"github.com/bradyloveland/pbcmanager/internal/runner"
)

func main() {
	os.Exit(runner.Main(runner.DefaultEnv(), os.Args[1:]))
}

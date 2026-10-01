// Command pbcwm-runner runs on each client. The server reaches it over SSH
// as the pbcwm account, which can run nothing else.
package main

import (
	"os"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/runner"
)

func main() {
	os.Exit(runner.Main(runner.DefaultEnv(), os.Args[1:]))
}

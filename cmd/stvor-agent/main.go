// Command stvor-agent is the Stvor portal agent on a machine: it polls the
// portal for the desired labels and applies them through the cream extension
// teleport (docs/specs/2026-10-18-stvor-agent-go.md).
package main

import (
	"os"

	"github.com/iamletenkov/stvor-agent/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

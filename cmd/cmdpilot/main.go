// CmdPilot is a lightweight dual-engine Windows command-line completion
// assistant (local knowledge base + OpenAI-compatible AI).
package main

import (
	"os"

	"github.com/qiutuan/CmdPilot/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}

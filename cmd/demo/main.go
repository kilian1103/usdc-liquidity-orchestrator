// Command demo runs worked examples (scenario full-funding, shortfall smoke, consolidate).
// Thin wrapper over internal/demorun. Prefer `usdc-liq demo` for the dual CLI surface.
package main

import (
	"fmt"
	"os"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/demorun"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/envfile"
)

func main() {
	if err := envfile.Load(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "envfile: %v\n", err)
		os.Exit(1)
	}
	os.Exit(demorun.Run(os.Stdout, os.Stderr))
}
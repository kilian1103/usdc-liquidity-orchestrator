// Command usdc-liq is the dual-surface CLI for non-custodial USDC liquidity planning.
// Peer of cmd/server (HTTP) and pkg/liquidity (library). See skills/usdc-liquidity/.
package main

import (
	"fmt"
	"os"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/envfile"
	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/liqcli"
)

func main() {
	if err := envfile.Load(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "envfile: %v\n", err)
		os.Exit(1)
	}
	os.Exit(liqcli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
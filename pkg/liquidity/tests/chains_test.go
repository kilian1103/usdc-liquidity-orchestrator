package liquidity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
)

func TestLookupByGatewayDomain(t *testing.T) {
	tests := []struct {
		name    string
		domain  int
		testnet bool
		want    string
		ok      bool
	}{
		{"base_sepolia", 6, true, "eip155:84532", true},
		{"base_mainnet", 6, false, "eip155:8453", true},
		{"arb_sepolia", 3, true, "eip155:421614", true},
		{"arb_mainnet", 3, false, "eip155:42161", true},
		{"arc_testnet", 26, true, "eip155:5042002", true},
		{"arc_mainnet_missing", 26, false, "", false},
		{"unknown_domain", 99, true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := liquidity.LookupByGatewayDomain(tc.domain, tc.testnet)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.want, c.CAIP2)
				assert.Equal(t, tc.domain, c.GatewayDomain)
				assert.Equal(t, tc.testnet, c.Testnet)
			}
		})
	}
}

func TestResolveChainRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		testnet bool
		want    string
		wantErr bool
	}{
		{"empty", "", true, "", true},
		{"caip2_arc", "eip155:5042002", true, "eip155:5042002", false},
		{"caip2_case", "EIP155:84532", false, "eip155:84532", false}, // LookupChain equalFold
		{"caip2_unknown", "eip155:1", true, "", true},
		{"name_arc", "arc-testnet", true, "eip155:5042002", false},
		{"name_base_sepolia", "base-sepolia", true, "eip155:84532", false},
		{"name_case", "Base-Sepolia", false, "eip155:84532", false},
		{"name_base_mainnet", "base", true, "eip155:8453", false},
		{"name_unknown", "not-a-chain", true, "", true},
		{"domain6_testnet", "6", true, "eip155:84532", false},
		{"domain6_mainnet", "6", false, "eip155:8453", false},
		{"domain3_testnet", "3", true, "eip155:421614", false},
		{"domain26_testnet", "26", true, "eip155:5042002", false},
		{"domain26_mainnet", "26", false, "", true},
		{"domain_unknown", "99", true, "", true},
		{"fuzzy_prefix_refuse", "base-sep", true, "", true},
		{"spaces_trim", "  6  ", true, "eip155:84532", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := liquidity.ResolveChainRef(tc.ref, tc.testnet)
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, liqerr.CodeInvalidQuery, liqerr.CodeOf(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, c.CAIP2)
		})
	}
}
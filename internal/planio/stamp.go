// Package planio is shared plan stamping and run helpers for HTTP and CLI surfaces.
package planio

import (
	"errors"

	liqerr "github.com/kilian1103/usdc-liquidity-orchestrator/pkg/errors"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/liquidity"
	"github.com/kilian1103/usdc-liquidity-orchestrator/pkg/types"
)

// StampOutcome classifies plan stamp results for HTTP status / CLI exit mapping.
type StampOutcome int

const (
	// StampOK is success (dry plan or full execute).
	StampOK StampOutcome = iota
	// StampPartial is execute with some tx hashes and an error.
	StampPartial
	// StampFail is pre-plan error or execute with zero hashes (force dry).
	StampFail
)

// ForceDryStamps sets dry-run inventory stamps on a wire plan.
func ForceDryStamps(wire types.Plan) types.Plan {
	wire.DryRun = true
	wire.Executed = false
	wire.InventoryAsserted = true
	wire.InventoryUnverified = true
	return wire
}

// SanitizeAPIError maps execute errors to stable code + fixed Message (no raw RPC).
func SanitizeAPIError(err error) *types.APIError {
	if err == nil {
		return nil
	}
	var e *liqerr.Error
	if errors.As(err, &e) && e != nil {
		code := e.Code
		if code == "" {
			code = liqerr.CodeLiquidityRailUnavailable
		}
		msg := e.Message
		if msg == "" {
			msg = "execute failed"
		}
		return &types.APIError{Code: code, Message: msg}
	}
	code := liqerr.CodeOf(err)
	if code == "" {
		code = liqerr.CodeLiquidityRailUnavailable
	}
	return &types.APIError{Code: code, Message: "execute failed"}
}

// StampPlan applies dry/execute stamps and receipt:
//
//	execute=false → force dry, no receipt, StampOK
//	success → dry_run=false executed=true + receipt hashes, StampOK
//	partial (hashes+err) → dry_run=false executed=false + receipt + error, StampPartial
//	fail zero hashes → force dry + error, StampFail
func StampPlan(wire types.Plan, execute bool, receipt liquidity.Receipt, execErr error) (types.PlanResponse, StampOutcome) {
	if !execute {
		return types.PlanResponse{Plan: ForceDryStamps(wire)}, StampOK
	}

	hashes := append([]string(nil), receipt.TxHashes...)
	apiErr := SanitizeAPIError(execErr)

	if execErr == nil {
		wire.DryRun = false
		wire.Executed = true
		wire.InventoryAsserted = true
		wire.InventoryUnverified = true
		resp := types.PlanResponse{Plan: wire}
		if len(hashes) > 0 {
			resp.Receipt = &types.ExecuteReceipt{TxHashes: hashes}
		}
		return resp, StampOK
	}

	if len(hashes) > 0 {
		wire.DryRun = false
		wire.Executed = false
		wire.InventoryAsserted = true
		wire.InventoryUnverified = true
		return types.PlanResponse{
			Plan:    wire,
			Receipt: &types.ExecuteReceipt{TxHashes: hashes},
			Error:   apiErr,
		}, StampPartial
	}

	return types.PlanResponse{
		Plan:  ForceDryStamps(wire),
		Error: apiErr,
	}, StampFail
}

// HTTPStatus maps stamp outcome to HTTP status codes.
func HTTPStatus(o StampOutcome) int {
	if o == StampOK {
		return 200
	}
	return 400
}

// ExitCode maps stamp outcome to process exit codes (0 success, 1 failure).
func ExitCode(o StampOutcome) int {
	if o == StampOK {
		return 0
	}
	return 1
}

// CodedPlanError builds a StampFail PlanResponse from a planning error (no execute).
func CodedPlanError(err error) (types.PlanResponse, StampOutcome) {
	code := liqerr.CodeOf(err)
	if code == "" {
		code = liqerr.CodeInvalidQuery
	}
	msg := err.Error()
	var e *liqerr.Error
	if errors.As(err, &e) && e != nil && e.Message != "" {
		msg = e.Message
	}
	return types.PlanResponse{
		Error: &types.APIError{Code: code, Message: msg},
	}, StampFail
}
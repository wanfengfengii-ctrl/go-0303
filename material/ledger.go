package material

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"shieldtunnel/domain"
)

// validateGasket enforces integer-millimetre conservation for one gasket bar:
// TotalLengthMM must equal the sum of every allocation (valid, lap, sample,
// remainder, loss), and every quantity must be non-negative.
func validateGasket(bar domain.GasketBar, allocs []domain.GasketAllocation) []domain.Reason {
	var reasons []domain.Reason
	if bar.TotalLengthMM <= 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "gasket bar total length must be positive"})
	}
	var sum int64
	for _, a := range allocs {
		if a.BarID != bar.ID {
			reasons = append(reasons, domain.Reason{Code: domain.CodeMaterialUnbalanced, Message: "allocation references a different bar"})
		}
		if a.LengthMM < 0 {
			reasons = append(reasons, domain.Reason{Code: domain.CodeMaterialUnbalanced, Message: "allocation length cannot be negative"})
			continue
		}
		sum += a.LengthMM
	}
	if bar.TotalLengthMM > 0 && sum != bar.TotalLengthMM {
		reasons = append(reasons, domain.Reason{Code: domain.CodeMaterialUnbalanced, Message: "gasket bar length is not conserved"})
	}
	return reasons
}

// validateAdhesive enforces integer-milligram conservation for one adhesive
// issue: TotalMg must equal AppliedMg+RetainedMg+RecoveredMg+LossMg with every
// component non-negative.
func validateAdhesive(issue domain.AdhesiveIssue) []domain.Reason {
	var reasons []domain.Reason
	if issue.TotalMg <= 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "adhesive total mass must be positive"})
	}
	for _, v := range []int64{issue.AppliedMg, issue.RetainedMg, issue.RecoveredMg, issue.LossMg} {
		if v < 0 {
			reasons = append(reasons, domain.Reason{Code: domain.CodeMaterialUnbalanced, Message: "adhesive component cannot be negative"})
		}
	}
	sum := issue.AppliedMg + issue.RetainedMg + issue.RecoveredMg + issue.LossMg
	if issue.TotalMg > 0 && sum != issue.TotalMg {
		reasons = append(reasons, domain.Reason{Code: domain.CodeMaterialUnbalanced, Message: "adhesive mass is not conserved"})
	}
	return reasons
}

// ledgerEntries derives immutable ledger rows from a balanced allocation.
func ledgerEntries(req AllocateRequest) []domain.MaterialLedgerEntry {
	var out []domain.MaterialLedgerEntry
	for _, a := range req.Allocations {
		out = append(out, domain.MaterialLedgerEntry{Kind: "gasket", DeltaMM: a.LengthMM, Operation: req.OperationID})
	}
	if req.AdhesiveIssue.AppliedMg > 0 {
		out = append(out, domain.MaterialLedgerEntry{Kind: "adhesive", DeltaMg: req.AdhesiveIssue.AppliedMg, Operation: req.OperationID})
	}
	if req.AdhesiveIssue.RetainedMg > 0 {
		out = append(out, domain.MaterialLedgerEntry{Kind: "adhesive", DeltaMg: req.AdhesiveIssue.RetainedMg, Operation: req.OperationID})
	}
	if req.AdhesiveIssue.RecoveredMg > 0 {
		out = append(out, domain.MaterialLedgerEntry{Kind: "adhesive", DeltaMg: req.AdhesiveIssue.RecoveredMg, Operation: req.OperationID})
	}
	if req.AdhesiveIssue.LossMg > 0 {
		out = append(out, domain.MaterialLedgerEntry{Kind: "adhesive", DeltaMg: req.AdhesiveIssue.LossMg, Operation: req.OperationID})
	}
	return out
}

// contentHash computes a canonical digest of the request fields that determine
// operation semantics (excluding the operation id itself), used for
// deterministic idempotency comparison.
func contentHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

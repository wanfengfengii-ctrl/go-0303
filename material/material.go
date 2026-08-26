// Package material implements the material-conservation and resource-lease
// manager (材料守恒与资源租约管理器). It keeps integer-millimetre gasket and
// integer-milligram adhesive ledgers, enforces single-slot gasket binding, and
// issues time-bounded single-holder leases for the six resource kinds.
package material

import "shieldtunnel/domain"

// MaterialManager is the stable interface for material ledgers and leases.
type MaterialManager interface {
	// Allocate atomically issues a gasket bar and an adhesive batch to a slot,
	// records the integer conservation ledger rows, binds the gasket to a
	// single slot and acquires any required lease in one transaction. It is
	// idempotent by OperationID and content hash.
	Allocate(req AllocateRequest) (AllocateResult, error)

	// AcquireLease atomically obtains a time-bounded single-holder lease.
	AcquireLease(req LeaseRequest) (domain.ResourceLease, error)

	// RenewLease extends a lease; only the current holder may renew.
	RenewLease(req LeaseRequest) (domain.ResourceLease, error)
}

// AllocateRequest is the atomic material allocation payload.
type AllocateRequest struct {
	OperationID   string                    `json:"operation_id"`
	RingID        string                    `json:"-"`
	Generation    domain.Generation         `json:"generation"`
	LogicalTime   int64                     `json:"logical_time"`
	Slot          domain.SegmentSlot        `json:"slot"`
	GasketBar     domain.GasketBar          `json:"gasket_bar"`
	Allocations   []domain.GasketAllocation `json:"allocations"`
	AdhesiveIssue domain.AdhesiveIssue      `json:"adhesive_issue"`
	Lease         *domain.ResourceLease     `json:"lease,omitempty"` // optional required lease
}

// AllocateResult is the committed receipt for a successful allocation.
type AllocateResult struct {
	Receipt   domain.OperationReceipt
	BarID     string
	LedgerSeq int64
}

// LeaseRequest is the payload for acquiring or renewing a lease.
type LeaseRequest struct {
	OperationID string              `json:"operation_id"`
	Resource    domain.ResourceKind `json:"resource"`
	ResourceID  string              `json:"resource_id"`
	Holder      string              `json:"holder"`
	Start       int64               `json:"start"`
	End         int64               `json:"end"`
}

// MaterialError builds a single-reason material error.
func MaterialError(code domain.ErrorCode, msg string) *domain.Error {
	return &domain.Error{Code: code, Reasons: []domain.Reason{{Code: code, Message: msg}}}
}

package material

import (
	"context"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// applyLease enforces the single-holder, time-bounded lease invariant for a
// resource. It is called inside a serialized store transaction, so the
// read-check-write sequence is atomic. renew selects the renew semantics:
// only the current holder may extend, and only before expiry.
func applyLease(ctx context.Context, tx *store.Tx, req LeaseRequest, renew bool) (domain.ResourceLease, error) {
	if req.End <= req.Start {
		return domain.ResourceLease{}, &domain.Error{
			Code:      domain.CodeLogicalTimeOrder,
			Operation: req.OperationID,
			Reasons:   []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "lease end must exceed start"}},
		}
	}
	if req.Start < 0 || req.End < 0 {
		return domain.ResourceLease{}, &domain.Error{
			Code:      domain.CodeNegativeTime,
			Operation: req.OperationID,
			Reasons:   []domain.Reason{{Code: domain.CodeNegativeTime, Message: "lease times cannot be negative"}},
		}
	}

	existing, err := tx.FindLease(ctx, req.Resource, req.ResourceID)
	if err != nil {
		return domain.ResourceLease{}, err
	}

	if existing == nil {
		lease := domain.ResourceLease{Resource: req.Resource, ResourceID: req.ResourceID, Holder: req.Holder, Start: req.Start, End: req.End}
		return lease, tx.ReplaceLease(ctx, lease)
	}

	if existing.Holder == req.Holder {
		if renew {
			if req.Start < existing.Start || req.End <= existing.End {
				return domain.ResourceLease{}, &domain.Error{
					Code:      domain.CodeLogicalTimeOrder,
					Operation: req.OperationID,
					Reasons:   []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "renew must extend the lease window"}},
				}
			}
			existing.End = req.End
			return *existing, tx.ReplaceLease(ctx, *existing)
		}
		// Same holder re-acquiring while still valid: idempotent no-op.
		if req.Start < existing.End {
			return *existing, nil
		}
	}

	// Different holder may only take over an expired lease.
	if req.Start >= existing.End {
		lease := domain.ResourceLease{Resource: req.Resource, ResourceID: req.ResourceID, Holder: req.Holder, Start: req.Start, End: req.End}
		return lease, tx.ReplaceLease(ctx, lease)
	}

	return domain.ResourceLease{}, &domain.Error{
		Code:      domain.CodeLeaseHolderMismatch,
		Operation: req.OperationID,
		Reasons:   []domain.Reason{{Code: domain.CodeLeaseHolderMismatch, Message: "resource is held by another operator"}},
	}
}

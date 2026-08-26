package material

import (
	"context"
	"encoding/json"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// Manager is the concrete material-conservation and resource-lease manager.
// It keeps integer-millimetre gasket and integer-milligram adhesive ledgers,
// enforces single-slot gasket binding, and issues time-bounded single-holder
// leases. Every allocation is atomic and idempotent by operation id.
type Manager struct {
	db *store.DB
}

// NewManager constructs the material manager over a store.
func NewManager(db *store.DB) *Manager {
	return &Manager{db: db}
}

// Allocate atomically issues a gasket bar and an adhesive batch to a slot,
// recording conservation ledger rows, single-slot binding and any required
// lease. It is idempotent by OperationID and content hash.
func (m *Manager) Allocate(req AllocateRequest) (AllocateResult, error) {
	hash := contentHash(struct {
		Generation    domain.Generation
		Slot          domain.SegmentSlot
		GasketBar     domain.GasketBar
		Allocations   []domain.GasketAllocation
		AdhesiveIssue domain.AdhesiveIssue
		Lease         *domain.ResourceLease
	}{req.Generation, req.Slot, req.GasketBar, req.Allocations, req.AdhesiveIssue, req.Lease})

	var out AllocateResult
	err := m.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()

		// Idempotency barrier.
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeResult(rc.Result, &out)
			}
			return &domain.Error{Code: domain.CodeIdempotentConflict, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeIdempotentConflict, Message: "operation id reused with different content"}}}
		}

		// Generation must match the locked ring task.
		task, err := tx.FindRingTaskByID(ctx, req.RingID)
		if err != nil {
			return err
		}
		if task == nil {
			return &domain.Error{Code: domain.CodeNotFound, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeNotFound, Message: "ring not locked"}}}
		}
		if task.Generation != req.Generation {
			return &domain.Error{Code: domain.CodeStaleGeneration, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeStaleGeneration, Generation: req.Generation, Message: "stale generation"}}}
		}

		// Conservation invariants.
		var reasons []domain.Reason
		reasons = append(reasons, validateGasket(req.GasketBar, req.Allocations)...)
		reasons = append(reasons, validateAdhesive(req.AdhesiveIssue)...)
		if len(reasons) > 0 {
			sorted := domain.SortReasons(reasons)
			return &domain.Error{Code: sorted[0].Code, Operation: req.OperationID, Reasons: sorted}
		}

		// Single-slot binding (unique bar identity).
		created, err := tx.CreateGasketBar(ctx, req.GasketBar, req.Slot)
		if err != nil {
			return err
		}
		if !created {
			return &domain.Error{Code: domain.CodeDuplicateBinding, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeDuplicateBinding, Message: "gasket bar already bound"}}}
		}

		// Optional required lease, in the same transaction.
		if req.Lease != nil {
			if _, err := applyLease(ctx, tx, LeaseRequest{
				OperationID: req.OperationID,
				Resource:    req.Lease.Resource,
				ResourceID:  req.Lease.ResourceID,
				Holder:      req.Lease.Holder,
				Start:       req.Lease.Start,
				End:         req.Lease.End,
			}, false); err != nil {
				return err
			}
		}

		entries := ledgerEntries(req)
		if err := tx.SaveLedgerEntries(ctx, entries); err != nil {
			return err
		}

		out = AllocateResult{
			Receipt:   domain.OperationReceipt{OperationID: req.OperationID, ContentHash: hash, Result: encodeResult(req.GasketBar.ID, int64(len(entries)))},
			BarID:     req.GasketBar.ID,
			LedgerSeq: int64(len(entries)),
		}
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, out.Receipt.Result); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindAllocate, Payload: out})
	})
	if err != nil {
		return AllocateResult{}, err
	}
	return out, nil
}

// AcquireLease obtains a time-bounded single-holder lease. It is idempotent by
// OperationID and content hash: a retry with the same operation id and the same
// lease request replays the committed lease; a retry that changes the request
// (e.g. a different holder or window) is rejected with idempotent_conflict and
// leaves the lease unchanged.
func (m *Manager) AcquireLease(req LeaseRequest) (domain.ResourceLease, error) {
	return m.applyLeaseTx(req, false)
}

// RenewLease extends a lease; only the current holder may renew. It carries the
// same operation-id idempotency and conflict semantics as AcquireLease.
func (m *Manager) RenewLease(req LeaseRequest) (domain.ResourceLease, error) {
	return m.applyLeaseTx(req, true)
}

// applyLeaseTx runs the lease transition inside a serialized transaction,
// guarded by the operation-id idempotency barrier so a reused operation_id can
// never silently retake or rehand a lease.
func (m *Manager) applyLeaseTx(req LeaseRequest, renew bool) (domain.ResourceLease, error) {
	hash := contentHash(struct {
		Kind       string
		Resource   domain.ResourceKind
		ResourceID string
		Holder     string
		Start      int64
		End        int64
	}{acquireKind(renew), req.Resource, req.ResourceID, req.Holder, req.Start, req.End})

	var lease domain.ResourceLease
	err := m.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()

		// Idempotency barrier: a committed operation_id is replayed on identical
		// content and rejected (without mutating the lease) on divergent content.
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeLease(rc.Result, &lease)
			}
			return &domain.Error{Code: domain.CodeIdempotentConflict, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeIdempotentConflict, Message: "operation id reused with different content"}}}
		}

		l, err := applyLease(ctx, tx, req, renew)
		if err != nil {
			return err
		}
		lease = l
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, encodeLease(l)); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindLease, Payload: lease})
	})
	return lease, err
}

// acquireKind returns the canonical operation kind for a lease request so that
// acquire and renew on the same payload hash to distinct digests.
func acquireKind(renew bool) string {
	if renew {
		return "renew"
	}
	return "acquire"
}

// encodeLease serializes a committed lease as an idempotent receipt result.
func encodeLease(l domain.ResourceLease) string {
	b, _ := json.Marshal(l)
	return string(b)
}

// decodeLease reconstructs a lease from a stored receipt.
func decodeLease(result string, out *domain.ResourceLease) error {
	return json.Unmarshal([]byte(result), out)
}

// LookupLease returns the current lease for a resource, or nil.
func (m *Manager) LookupLease(ctx context.Context, resource domain.ResourceKind, id string) (*domain.ResourceLease, error) {
	var lease *domain.ResourceLease
	err := m.db.WithTx(ctx, func(tx *store.Tx) error {
		l, err := tx.FindLease(ctx, resource, id)
		lease = l
		return err
	})
	return lease, err
}

// encodeResult serializes an allocation receipt result.
func encodeResult(barID string, ledgerSeq int64) string {
	b, _ := json.Marshal(map[string]any{"bar_id": barID, "ledger_seq": ledgerSeq})
	return string(b)
}

// decodeResult reconstructs an AllocateResult from a stored receipt.
func decodeResult(result string, out *AllocateResult) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		return err
	}
	out.BarID, _ = m["bar_id"].(string)
	if v, ok := m["ledger_seq"].(float64); ok {
		out.LedgerSeq = int64(v)
	}
	return nil
}

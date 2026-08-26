package httpapi

import (
	"context"
	"net/http"
	"testing"

	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/store"
)

func TestModel_AllocateRejectsConflictingEmbeddedLeaseAtomically(t *testing.T) {
	tests := []struct {
		name        string
		leaseStart  int64
		leaseEnd    int64
		logicalTime int64
	}{
		{name: "active_clean_bay_lease_held_by_another_operator", leaseStart: 10, leaseEnd: 30, logicalTime: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fullService(t)
			status, body := doJSON(t, srv, http.MethodPost, "/api/rings", lockBody(t))
			if status != http.StatusOK {
				t.Fatalf("lock status %d body %v", status, body)
			}
			ringID := body["task"].(map[string]any)["id"].(string)

			status, body = doJSON(t, srv, http.MethodPost, "/api/leases", material.LeaseRequest{
				OperationID: "lease-current-holder",
				Resource:    domain.ResourceCleanBay,
				ResourceID:  ringID,
				Holder:      "operator-a",
				Start:       tt.leaseStart,
				End:         tt.leaseEnd,
			})
			if status != http.StatusOK {
				t.Fatalf("seed lease status %d body %v", status, body)
			}

			const allocationOperation = "allocate-with-conflicting-lease"
			const barID = "gasket-conflicting-lease"
			status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/materials/allocate", material.AllocateRequest{
				OperationID: allocationOperation,
				Generation:  1,
				LogicalTime: tt.logicalTime,
				Slot:        domain.SegmentSlot{SegmentSeq: 0},
				GasketBar:   domain.GasketBar{ID: barID, Batch: "GASKET-2026A", TotalLengthMM: 1000},
				Allocations: []domain.GasketAllocation{
					{BarID: barID, Kind: "valid", LengthMM: 800},
					{BarID: barID, Kind: "lap", LengthMM: 100},
					{BarID: barID, Kind: "sample", LengthMM: 50},
					{BarID: barID, Kind: "remainder", LengthMM: 30},
					{BarID: barID, Kind: "loss", LengthMM: 20},
				},
				AdhesiveIssue: domain.AdhesiveIssue{
					Batch: "ADH-2026B", Generation: 1, TotalMg: 1000,
					AppliedMg: 700, RetainedMg: 100, RecoveredMg: 100, LossMg: 100,
				},
				Lease: &domain.ResourceLease{
					Resource: domain.ResourceCleanBay, ResourceID: ringID,
					Holder: "operator-b", Start: tt.leaseStart, End: tt.leaseEnd,
				},
			})
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("conflicting allocation status %d want 422; body %v", status, body)
			}
			if body["operation_id"] != allocationOperation {
				t.Fatalf("operation_id %v want %q", body["operation_id"], allocationOperation)
			}

			db := srv.Store.(*store.DB)
			ctx := context.Background()
			if err := db.WithTx(ctx, func(tx *store.Tx) error {
				bar, err := tx.FindGasketBar(ctx, barID)
				if err != nil {
					return err
				}
				if bar != nil {
					t.Fatalf("gasket binding became visible after lease conflict: %+v", bar)
				}
				ledger, err := tx.ListLedger(ctx)
				if err != nil {
					return err
				}
				for _, entry := range ledger {
					if entry.Operation == allocationOperation {
						t.Fatalf("material ledger entry became visible after lease conflict: %+v", entry)
					}
				}
				receipt, err := tx.FindReceipt(ctx, allocationOperation)
				if err != nil {
					return err
				}
				if receipt != nil {
					t.Fatalf("allocation receipt became visible after lease conflict: %+v", receipt)
				}
				lease, err := tx.FindLease(ctx, domain.ResourceCleanBay, ringID)
				if err != nil {
					return err
				}
				if lease == nil || lease.Holder != "operator-a" || lease.Start != tt.leaseStart || lease.End != tt.leaseEnd {
					t.Fatalf("conflicting allocation changed existing lease: %+v", lease)
				}
				return nil
			}); err != nil {
				t.Fatalf("inspect committed state: %v", err)
			}

			events, err := db.Replay(ctx, 0)
			if err != nil {
				t.Fatalf("replay events: %v", err)
			}
			for _, event := range events {
				if event.Operation == allocationOperation {
					t.Fatalf("allocation event became visible after lease conflict: %+v", event)
				}
			}
		})
	}
}

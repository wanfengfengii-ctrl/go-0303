package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/verdict"
)

func TestModel_RetestGasketBatchBindingsStayWithinRing(t *testing.T) {
	tests := []struct {
		name         string
		secondRing   int
		secondSlot   int
		wantAffected []int
	}{
		{
			name:         "same ring shared batch expands retest",
			secondRing:   1,
			secondSlot:   4,
			wantAffected: []int{0, 1, 4, 5},
		},
		{
			name:         "different ring shared batch is isolated",
			secondRing:   2,
			secondSlot:   104,
			wantAffected: []int{0, 1, 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fullService(t)

			lock := func(ringNo int, seqOffset int) string {
				t.Helper()
				req := lockBody(t)
				req.OperationID = fmt.Sprintf("lock-%d", ringNo)
				req.RingNo = domain.RingNo(ringNo)
				for i := range req.Segments {
					req.Segments[i].Seq += seqOffset
				}
				for i := range req.Joints {
					req.Joints[i].EdgeA.SegmentSeq += seqOffset
					req.Joints[i].EdgeB.SegmentSeq += seqOffset
				}
				status, body := doJSON(t, srv, http.MethodPost, "/api/rings", req)
				if status != http.StatusOK {
					t.Fatalf("lock ring %d: status %d body %v", ringNo, status, body)
				}
				task, ok := body["task"].(map[string]any)
				if !ok {
					t.Fatalf("lock ring %d missing task: %v", ringNo, body)
				}
				id, ok := task["id"].(string)
				if !ok || id == "" {
					t.Fatalf("lock ring %d missing id: %v", ringNo, task)
				}
				return id
			}

			ringIDs := map[int]string{1: lock(1, 0), 2: lock(2, 100)}
			allocate := func(op, ringID, barID string, slot int) {
				t.Helper()
				status, body := doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/materials/allocate", material.AllocateRequest{
					OperationID: op,
					Generation:  1,
					LogicalTime: 1,
					Slot:        domain.SegmentSlot{SegmentSeq: slot},
					GasketBar:   domain.GasketBar{ID: barID, Batch: "shared-batch", TotalLengthMM: 100},
					Allocations: []domain.GasketAllocation{{BarID: barID, Kind: "valid", LengthMM: 100}},
					AdhesiveIssue: domain.AdhesiveIssue{
						Batch: "adhesive-" + barID, Generation: 1, TotalMg: 100, AppliedMg: 100,
					},
				})
				if status != http.StatusOK {
					t.Fatalf("allocate %s: status %d body %v", barID, status, body)
				}
			}

			allocate("allocate-source", ringIDs[1], "bar-source", 0)
			allocate("allocate-peer", ringIDs[tt.secondRing], "bar-peer", tt.secondSlot)

			retestReq := verdict.RetestRequest{OperationID: "retest-ring-1", Generation: 1, Source: "joint_crack:0"}
			var first map[string]any
			for attempt := 0; attempt < 2; attempt++ {
				status, body := doJSON(t, srv, http.MethodPost, "/api/rings/"+ringIDs[1]+"/retests", retestReq)
				if status != http.StatusOK {
					t.Fatalf("retest attempt %d: status %d body %v", attempt+1, status, body)
				}
				retest, ok := body["retest"].(map[string]any)
				if !ok {
					t.Fatalf("retest attempt %d missing result: %v", attempt+1, body)
				}
				if attempt == 0 {
					first = retest
				} else if !reflect.DeepEqual(retest, first) {
					t.Fatalf("idempotent retest changed: first %v second %v", first, retest)
				}
			}

			if got := int(first["generation"].(float64)); got != 2 {
				t.Fatalf("generation %d want 2", got)
			}
			raw, ok := first["affected"].([]any)
			if !ok {
				t.Fatalf("affected has type %T: %v", first["affected"], first["affected"])
			}
			got := make([]int, len(raw))
			for i := range raw {
				got[i] = int(raw[i].(float64))
			}
			if !reflect.DeepEqual(got, tt.wantAffected) {
				t.Fatalf("affected %v want %v", got, tt.wantAffected)
			}
		})
	}
}

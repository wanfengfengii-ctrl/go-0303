package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/process"
)

func TestModel_EvidenceIdempotencyIsScopedToTargetRing(t *testing.T) {
	cases := []struct {
		name       string
		targetRing int
	}{
		{name: "same ring replays the original receipt", targetRing: 0},
		{name: "another ring cannot silently replay success", targetRing: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fullService(t)
			ringIDs := make([]string, 2)
			for i := range ringIDs {
				lock := lockBody(t)
				lock.OperationID = "lock-idempotency-ring-" + string(rune('1'+i))
				lock.RingNo = domain.RingNo(i + 1)
				status, body := doJSON(t, srv, http.MethodPost, "/api/rings", lock)
				if status != http.StatusOK {
					t.Fatalf("lock ring %d: status %d body %v", i+1, status, body)
				}
				task, ok := body["task"].(map[string]any)
				if !ok {
					t.Fatalf("lock ring %d response missing task: %v", i+1, body)
				}
				ringIDs[i], ok = task["id"].(string)
				if !ok || ringIDs[i] == "" {
					t.Fatalf("lock ring %d response missing task id: %v", i+1, body)
				}

				status, body = doJSON(t, srv, http.MethodPost, "/api/leases", material.LeaseRequest{
					OperationID: "lease-idempotency-ring-" + string(rune('1'+i)),
					Resource:    domain.ResourceCleanBay,
					ResourceID:  ringIDs[i],
					Holder:      "operator-1",
					Start:       0,
					End:         100,
				})
				if status != http.StatusOK {
					t.Fatalf("lease ring %d: status %d body %v", i+1, status, body)
				}
			}

			evidence := process.EvidenceRequest{
				OperationID: "shared-clean-operation",
				Generation:  1,
				LogicalTime: 10,
				Operator:    "operator-1",
				Process: &domain.ProcessEvidence{
					Kind:         "clean",
					Generation:   1,
					LogicalTime:  10,
					InstrumentID: "clean-gauge-1",
				},
			}
			firstStatus, firstBody := doJSON(t, srv, http.MethodPost, "/api/rings/"+ringIDs[0]+"/evidence", evidence)
			if firstStatus != http.StatusOK || firstBody["code"] != "ok" {
				t.Fatalf("first submission: status %d body %v", firstStatus, firstBody)
			}

			secondStatus, secondBody := doJSON(t, srv, http.MethodPost, "/api/rings/"+ringIDs[tc.targetRing]+"/evidence", evidence)
			prefixes := make([]int, len(ringIDs))
			for i, id := range ringIDs {
				prefix, err := srv.Process.Prefix(id, 1)
				if err != nil {
					t.Fatalf("prefix ring %d: %v", i+1, err)
				}
				prefixes[i] = prefix
			}

			if tc.targetRing == 0 {
				if secondStatus != http.StatusOK || secondBody["code"] != "ok" {
					t.Fatalf("same-ring replay: status %d body %v", secondStatus, secondBody)
				}
				if !reflect.DeepEqual(secondBody["receipt"], firstBody["receipt"]) {
					t.Fatalf("same-ring replay receipt %v want original %v", secondBody["receipt"], firstBody["receipt"])
				}
				if prefixes[0] != 1 || prefixes[1] != 0 {
					t.Fatalf("same-ring replay prefixes %v want [1 0]", prefixes)
				}
				return
			}

			switch secondBody["code"] {
			case "ok":
				if secondStatus != http.StatusOK || prefixes[1] != 1 {
					t.Fatalf("cross-ring success did not advance target: status %d prefixes %v body %v", secondStatus, prefixes, secondBody)
				}
			case "idempotent_conflict":
				if secondStatus == http.StatusOK || prefixes[1] != 0 {
					t.Fatalf("cross-ring conflict changed target or returned success: status %d prefixes %v body %v", secondStatus, prefixes, secondBody)
				}
			default:
				t.Fatalf("cross-ring reuse: status %d code %v, want committed target evidence or idempotent_conflict", secondStatus, secondBody["code"])
			}
		})
	}
}

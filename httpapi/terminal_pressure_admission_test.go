package httpapi

import (
	"context"
	"net/http"
	"testing"

	"shieldtunnel/domain"
	"shieldtunnel/store"
	"shieldtunnel/verdict"
)

func TestModel_TerminalAdmissionPressureDecayGate(t *testing.T) {
	tests := []struct {
		name       string
		traces     []domain.PressureTrace
		wantStatus int
		wantCode   domain.ErrorCode
		wantAdmit  bool
	}{
		{
			name: "decay above locked maximum is rejected atomically",
			traces: []domain.PressureTrace{
				{Bay: 0, LogicalTime: 0, Pressure: 1000000},
				{Bay: 0, LogicalTime: 10, Pressure: 0},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   domain.CodeOutOfTolerance,
		},
		{
			name: "decay exactly at locked maximum remains admissible",
			traces: []domain.PressureTrace{
				{Bay: 0, LogicalTime: 0, Pressure: 1000000},
				{Bay: 0, LogicalTime: 10, Pressure: 500000},
			},
			wantStatus: http.StatusOK,
			wantCode:   domain.CodeOK,
			wantAdmit:  true,
		},
		{
			name:       "no pressure points preserves admission behavior",
			wantStatus: http.StatusOK,
			wantCode:   domain.CodeOK,
			wantAdmit:  true,
		},
		{
			name: "one pressure point preserves admission behavior",
			traces: []domain.PressureTrace{
				{Bay: 0, LogicalTime: 0, Pressure: 1000000},
			},
			wantStatus: http.StatusOK,
			wantCode:   domain.CodeOK,
			wantAdmit:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fullService(t)
			status, body := doJSON(t, srv, http.MethodPost, "/api/rings", lockBody(t))
			if status != http.StatusOK {
				t.Fatalf("lock status %d body %v", status, body)
			}
			task, ok := body["task"].(map[string]any)
			if !ok {
				t.Fatalf("lock response missing task: %v", body)
			}
			ringID, ok := task["id"].(string)
			if !ok || ringID == "" {
				t.Fatalf("lock response has invalid ring id: %v", task["id"])
			}

			for i, trace := range tt.traces {
				status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/pressure-traces", verdict.PressureTraceRequest{
					OperationID: "trace-" + string(rune('a'+i)),
					Generation:  1,
					Trace:       trace,
				})
				if status != http.StatusOK {
					t.Fatalf("trace %d status %d body %v", i, status, body)
				}
			}

			for i, reviewer := range []string{"qualified-reviewer-a", "qualified-reviewer-b"} {
				status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/reviews", verdict.ReviewRequest{
					OperationID: "review-" + string(rune('a'+i)),
					Generation:  1,
					Reviewer:    reviewer,
					Qualified:   true,
					Approved:    true,
				})
				if status != http.StatusOK {
					t.Fatalf("review %d status %d body %v", i, status, body)
				}
			}

			db, ok := srv.Store.(*store.DB)
			if !ok {
				t.Fatalf("service store type %T, want *store.DB", srv.Store)
			}
			before, err := db.LastSeq(context.Background())
			if err != nil {
				t.Fatalf("last sequence before decision: %v", err)
			}

			const decisionOperation = "terminal-admit"
			status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/terminal-decisions", verdict.DecisionRequest{
				OperationID: decisionOperation,
				Generation:  1,
				Kind:        "admit",
			})
			if status != tt.wantStatus {
				t.Fatalf("decision status %d want %d body %v", status, tt.wantStatus, body)
			}
			if body["code"] != string(tt.wantCode) {
				t.Fatalf("decision code %v want %s body %v", body["code"], tt.wantCode, body)
			}

			if tt.wantAdmit {
				decision, ok := body["decision"].(map[string]any)
				if !ok || decision["kind"] != "admit" || decision["credential"] == "" {
					t.Fatalf("successful admission missing credential: %v", body)
				}
				return
			}

			after, err := db.LastSeq(context.Background())
			if err != nil {
				t.Fatalf("last sequence after rejected decision: %v", err)
			}
			if after != before {
				t.Fatalf("rejected decision appended event: sequence changed from %d to %d", before, after)
			}

			var terminal *domain.TerminalDecision
			var receipt *domain.OperationReceipt
			err = db.WithTx(context.Background(), func(tx *store.Tx) error {
				var txErr error
				terminal, txErr = tx.FindTerminal(context.Background(), ringID)
				if txErr != nil {
					return txErr
				}
				receipt, txErr = tx.FindReceipt(context.Background(), decisionOperation)
				return txErr
			})
			if err != nil {
				t.Fatalf("inspect rejected decision state: %v", err)
			}
			if terminal != nil {
				t.Fatalf("rejected decision persisted terminal or credential: %+v", terminal)
			}
			if receipt != nil {
				t.Fatalf("rejected decision persisted operation receipt: %+v", receipt)
			}
		})
	}
}

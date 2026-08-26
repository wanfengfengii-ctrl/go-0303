package httpapi

import (
	"net/http"
	"testing"

	"shieldtunnel/verdict"
)

func TestModel_TerminalDecisionRequiresCurrentGeneration(t *testing.T) {
	tests := []struct {
		name           string
		kind           string
		missingRing    bool
		wantRejectCode string
	}{
		{name: "stale admit", kind: "admit", wantRejectCode: "stale_generation"},
		{name: "stale isolate", kind: "isolate", wantRejectCode: "stale_generation"},
		{name: "stale cancel", kind: "cancel", wantRejectCode: "stale_generation"},
		{name: "missing ring admit", kind: "admit", missingRing: true, wantRejectCode: "not_found"},
		{name: "missing ring isolate", kind: "isolate", missingRing: true, wantRejectCode: "not_found"},
		{name: "missing ring cancel", kind: "cancel", missingRing: true, wantRejectCode: "not_found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := fullService(t)
			ringID := "ring-that-does-not-exist"
			if !tc.missingRing {
				status, body := doJSON(t, srv, http.MethodPost, "/api/rings", lockBody(t))
				if status != http.StatusOK {
					t.Fatalf("lock status %d body %v", status, body)
				}
				ringID = body["task"].(map[string]any)["id"].(string)
			}

			status, body := doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/terminal-decisions", verdict.DecisionRequest{
				OperationID: "wrong-generation-" + tc.kind,
				Generation:  99,
				Kind:        tc.kind,
			})
			if status != http.StatusUnprocessableEntity || body["code"] != tc.wantRejectCode {
				t.Fatalf("wrong-generation decision status %d code %v, want 422/%s; body %v", status, body["code"], tc.wantRejectCode, body)
			}

			if tc.missingRing {
				return
			}
			if tc.kind == "admit" {
				for _, reviewer := range []string{"reviewer-1", "reviewer-2"} {
					status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/reviews", verdict.ReviewRequest{
						OperationID: "review-" + reviewer,
						Generation:  1,
						Reviewer:    reviewer,
						Qualified:   true,
						Approved:    true,
					})
					if status != http.StatusOK {
						t.Fatalf("review status %d body %v", status, body)
					}
				}
			}

			status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+ringID+"/terminal-decisions", verdict.DecisionRequest{
				OperationID: "current-generation-" + tc.kind,
				Generation:  1,
				Kind:        tc.kind,
			})
			if status != http.StatusOK {
				t.Fatalf("current-generation decision status %d body %v", status, body)
			}
			decision, ok := body["decision"].(map[string]any)
			if !ok || decision["kind"] != tc.kind || decision["generation"] != float64(1) {
				t.Fatalf("decision %v, want kind %q at generation 1", body["decision"], tc.kind)
			}
		})
	}
}

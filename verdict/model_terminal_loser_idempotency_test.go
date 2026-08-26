package verdict

import (
	"errors"
	"testing"

	"shieldtunnel/domain"
)

func TestModel_TerminalLoserReceiptRejectsTamperedReplay(t *testing.T) {
	h := newHarness(t)
	h.review(t, "admitter-a", true, true)
	h.review(t, "admitter-b", true, true)

	winner, err := h.arb.Decide(DecisionRequest{
		OperationID: "terminal-winner",
		RingID:      h.id,
		Generation:  1,
		Kind:        "admit",
	})
	if err != nil {
		t.Fatalf("commit admit terminal: %v", err)
	}
	if winner.Kind != "admit" || winner.Credential == "" {
		t.Fatalf("winner = %+v, want admitted terminal with credential", winner)
	}

	late := DecisionRequest{
		OperationID: "late-terminal-operation",
		RingID:      h.id,
		Generation:  1,
		Kind:        "isolate",
	}

	cases := []struct {
		name     string
		request  DecisionRequest
		wantCode domain.ErrorCode
	}{
		{name: "loser reads committed terminal and records receipt", request: late},
		{name: "identical loser replay returns recorded result", request: late},
		{name: "tampered loser replay is a stable conflict", request: DecisionRequest{
			OperationID: late.OperationID,
			RingID:      late.RingID,
			Generation:  late.Generation,
			Kind:        "cancel",
		}, wantCode: domain.CodeIdempotentConflict},
		{name: "committed terminal remains immutable", request: DecisionRequest{
			OperationID: "terminal-observer",
			RingID:      late.RingID,
			Generation:  late.Generation,
			Kind:        "cancel",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.arb.Decide(tc.request)
			if tc.wantCode != "" {
				var domainErr *domain.Error
				if !errors.As(err, &domainErr) {
					t.Fatalf("Decide() error = %v, want domain error %q", err, tc.wantCode)
				}
				if domainErr.Code != tc.wantCode || domainErr.Operation != tc.request.OperationID {
					t.Fatalf("Decide() error = %+v, want code %q for operation %q", domainErr, tc.wantCode, tc.request.OperationID)
				}
				return
			}

			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if got != winner {
				t.Fatalf("Decide() = %+v, want original terminal %+v", got, winner)
			}
		})
	}
}

package verdict

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// Arbiter is the concrete watertight re-inspection and terminal arbitrator.
// It stores compartment pressure traces, performs fixed-point integration and
// decay judgement, deterministically propagates anomaly retest sets, isolates
// retest generations and arbitrates the single irreversible terminal decision
// through a single-writer database barrier.
type Arbiter struct {
	db *store.DB
}

// NewArbitrator constructs the arbitrator over a store.
func NewArbiter(db *store.DB) *Arbiter {
	return &Arbiter{db: db}
}

// AddPressureTrace appends one compartment pressure reading, validating the
// time axis and fixed-point arithmetic (integration and decay).
func (a *Arbiter) AddPressureTrace(req PressureTraceRequest) (domain.OperationReceipt, error) {
	hash := contentHash(req)
	var receipt domain.OperationReceipt
	err := a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				receipt = *rc
				return nil
			}
			return conflict(req.OperationID)
		}
		if err := checkRing(ctx, tx, req.RingID, req.Generation, req.OperationID); err != nil {
			return err
		}
		if req.Trace.LogicalTime < 0 {
			return &domain.Error{Code: domain.CodeNegativeTime, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeNegativeTime, Message: "pressure time cannot be negative"}}}
		}
		prior, err := tx.ListTraces(ctx, req.RingID, req.Generation)
		if err != nil {
			return err
		}
		if len(prior) > 0 && req.Trace.LogicalTime < prior[len(prior)-1].LogicalTime {
			return &domain.Error{Code: domain.CodeLogicalTimeOrder, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "pressure time cannot regress"}}}
		}
		if err := tx.SaveTrace(ctx, req.RingID, req.Generation, req.Trace); err != nil {
			return err
		}
		all := append(prior, req.Trace)
		if _, err := integrate(all); err != nil {
			return arithmeticErr(req.OperationID, err)
		}
		if _, err := decayRate(all); err != nil {
			return arithmeticErr(req.OperationID, err)
		}
		receipt = domain.OperationReceipt{OperationID: req.OperationID, ContentHash: hash, Result: "ok"}
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, "ok"); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindTrace, Payload: req.Trace})
	})
	return receipt, err
}

// PropagateRetest deterministically derives and stores the unique sorted retest
// set from an anomaly source and creates a new generation.
func (a *Arbiter) PropagateRetest(req RetestRequest) (domain.RetestCase, error) {
	hash := contentHash(req)
	var out domain.RetestCase
	err := a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeRetest(rc.Result, &out)
			}
			return conflict(req.OperationID)
		}
		if err := checkRing(ctx, tx, req.RingID, req.Generation, req.OperationID); err != nil {
			return err
		}
		task, err := tx.FindRingTaskByID(ctx, req.RingID)
		if err != nil {
			return err
		}
		bindings, err := tx.ListGasketBindings(ctx)
		if err != nil {
			return err
		}
		affected := propagate(*task, bindings, req.Source)
		newGen := req.Generation + 1
		out = domain.RetestCase{
			ID:         retestID(req.RingID, newGen, req.Source),
			Source:     req.Source,
			Affected:   affected,
			Generation: newGen,
			Resolved:   false,
		}
		if err := tx.SaveRetest(ctx, req.RingID, out); err != nil {
			return err
		}
		// Bump the ring task generation so old-generation receipts become stale.
		task.Generation = newGen
		if err := tx.SaveRingTask(ctx, *task); err != nil {
			return err
		}
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, encodeRetest(out)); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindRetest, Payload: out})
	})
	if err != nil {
		return domain.RetestCase{}, err
	}
	return out, nil
}

// SubmitReview records one independent reviewer sign-off, rejecting duplicate
// reviewers. A second qualified approver closes any open retest generation.
func (a *Arbiter) SubmitReview(req ReviewRequest) (domain.Review, error) {
	hash := contentHash(req)
	var out domain.Review
	err := a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeReview(rc.Result, &out)
			}
			return conflict(req.OperationID)
		}
		if err := checkRing(ctx, tx, req.RingID, req.Generation, req.OperationID); err != nil {
			return err
		}
		existing, err := tx.ListReviews(ctx, req.RingID, req.Generation)
		if err != nil {
			return err
		}
		for _, r := range existing {
			if r.Reviewer == req.Reviewer {
				return &domain.Error{Code: domain.CodeDuplicateReviewer, Operation: req.OperationID,
					Reasons: []domain.Reason{{Code: domain.CodeDuplicateReviewer, Message: "reviewer already signed"}}}
			}
		}
		out = domain.Review{Reviewer: req.Reviewer, Qualified: req.Qualified, Generation: req.Generation, Approved: req.Approved}
		if err := tx.SaveReview(ctx, req.RingID, out); err != nil {
			return err
		}
		// Close an open retest once two distinct qualified reviewers approve.
		if req.Qualified && req.Approved {
			if retest, err := tx.FindRetest(ctx, req.RingID); err == nil && retest != nil && retest.Generation == req.Generation && !retest.Resolved {
				approved := 0
				for _, r := range append(existing, out) {
					if r.Qualified && r.Approved {
						approved++
					}
				}
				if approved >= 2 {
					_ = tx.MarkRetestResolved(ctx, retest.ID)
				}
			}
		}
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, encodeReview(out)); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindReview, Payload: out})
	})
	if err != nil {
		return domain.Review{}, err
	}
	return out, nil
}

// Decide competes to submit the single terminal decision. Only one of
// admit/isolate/cancel may commit; losers read the committed decision.
func (a *Arbiter) Decide(req DecisionRequest) (domain.TerminalDecision, error) {
	hash := contentHash(req)
	var out domain.TerminalDecision
	err := a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()
		// Idempotency by operation id: identical content replays the original
		// receipt, different content is an idempotent-conflict. This must run
		// before the existing-terminal fast path so that a losing competitor
		// (or any replay) is bound to its operation id exactly like a winner,
		// and a later content-tampered replay of the same operation id is
		// detectable rather than silently returning the existing terminal.
		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeDecision(rc.Result, &out)
			}
			return conflict(req.OperationID)
		}

		if req.Kind != "admit" && req.Kind != "isolate" && req.Kind != "cancel" {
			return &domain.Error{Code: domain.CodeInternal, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: "unknown terminal kind"}}}
		}

		if req.Kind == "admit" {
			if err := a.validateAdmission(ctx, tx, req); err != nil {
				return err
			}
		}

		credential := ""
		if req.Kind == "admit" {
			credential = credentialFor(req.RingID, req.Generation)
		}
		decision, _, err := tx.SaveTerminal(ctx, req.RingID, req.Generation, req.Kind, credential)
		if err != nil {
			return err
		}
		out = *decision
		// Both the winner (terminal created) and a losing competitor (existing
		// terminal read back) record a receipt bound to their own operation id and
		// append the decide event. The loser returns the already-committed decision
		// rather than re-issuing a credential, yet its operation id is now bound to
		// the decision content it observed — so a later content-tampered replay of
		// that same operation id (e.g. the same op replayed as "isolate" after it
		// already lost as "admit") is flagged as an idempotent-conflict instead of
		// silently echoing the existing terminal again.
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, encodeDecision(out)); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindDecide, Payload: out})
	})
	if err != nil {
		return domain.TerminalDecision{}, err
	}
	return out, nil
}

// validateAdmission enforces the admission gate for a "admit" decision.
func (a *Arbiter) validateAdmission(ctx context.Context, tx *store.Tx, req DecisionRequest) error {
	retest, err := tx.FindRetest(ctx, req.RingID)
	if err != nil {
		return err
	}
	if retest != nil {
		if retest.Generation > req.Generation {
			return &domain.Error{Code: domain.CodeStaleGeneration, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeStaleGeneration, Generation: req.Generation, Message: "newer retest generation exists"}}}
		}
		if retest.Generation == req.Generation && !retest.Resolved {
			return &domain.Error{Code: domain.CodeRetestOpen, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeRetestOpen, Message: "retest re-verification not closed"}}}
		}
	}

	reviews, err := tx.ListReviews(ctx, req.RingID, req.Generation)
	if err != nil {
		return err
	}
	qualified := 0
	for _, r := range reviews {
		if r.Qualified && r.Approved {
			qualified++
		}
	}
	if qualified < 2 {
		return &domain.Error{Code: domain.CodeNotQualified, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeNotQualified, Message: "requires two independent qualified approvers"}}}
	}
	traces, err := tx.ListTraces(ctx, req.RingID, req.Generation)
	if err != nil {
		return err
	}
	if len(traces) >= 2 {
		task, err := tx.FindRingTaskByID(ctx, req.RingID)
		if err != nil {
			return err
		}
		rate, err := decayRate(traces)
		if err != nil {
			return arithmeticErr(req.OperationID, err)
		}
		if rate > task.Rule.Thresholds.DecayRateMax {
			return &domain.Error{Code: domain.CodeOutOfTolerance, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeOutOfTolerance, Message: "compartment leak decay rate out of tolerance"}}}
		}
	}
	return nil
}

// checkRing verifies a ring exists and the generation matches.
func checkRing(ctx context.Context, tx *store.Tx, ringID string, gen domain.Generation, op string) error {
	task, err := tx.FindRingTaskByID(ctx, ringID)
	if err != nil {
		return err
	}
	if task == nil {
		return &domain.Error{Code: domain.CodeNotFound, Operation: op,
			Reasons: []domain.Reason{{Code: domain.CodeNotFound, Message: "ring not locked"}}}
	}
	if task.Generation != gen {
		return &domain.Error{Code: domain.CodeStaleGeneration, Operation: op,
			Reasons: []domain.Reason{{Code: domain.CodeStaleGeneration, Generation: gen, Message: "stale generation"}}}
	}
	return nil
}

func conflict(op string) error {
	return &domain.Error{Code: domain.CodeIdempotentConflict, Operation: op,
		Reasons: []domain.Reason{{Code: domain.CodeIdempotentConflict, Message: "operation id reused with different content"}}}
}

func arithmeticErr(op string, err error) error {
	code := domain.CodeOverflow
	if err == domain.ErrFixedDivideByZero {
		code = domain.CodeDivideByZero
	}
	return &domain.Error{Code: code, Operation: op, Reasons: []domain.Reason{{Code: code, Message: err.Error()}}}
}

func retestID(ringID string, gen domain.Generation, source string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", ringID, gen, source)))
	return fmt.Sprintf("%s/%d/%s", ringID, gen, hex.EncodeToString(h[:])[:12])
}

func credentialFor(ringID string, gen domain.Generation) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("admit|%s|%d", ringID, gen)))
	return hex.EncodeToString(h[:])[:24]
}

func contentHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func encodeRetest(r domain.RetestCase) string { b, _ := json.Marshal(r); return string(b) }
func encodeReview(r domain.Review) string     { b, _ := json.Marshal(r); return string(b) }
func encodeDecision(d domain.TerminalDecision) string {
	b, _ := json.Marshal(d)
	return string(b)
}
func decodeRetest(s string, out *domain.RetestCase) error { return json.Unmarshal([]byte(s), out) }
func decodeReview(s string, out *domain.Review) error     { return json.Unmarshal([]byte(s), out) }
func decodeDecision(s string, out *domain.TerminalDecision) error {
	return json.Unmarshal([]byte(s), out)
}

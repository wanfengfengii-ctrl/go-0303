// Package verdict implements the watertight re-inspection and terminal
// arbitrator (水密复验与终局仲裁器). It stores compartment pressure traces,
// performs integer fixed-point integration and decay judgement, deterministically
// propagates anomaly retest sets, isolates old/new retest generations, and
// arbitrates the single irreversible terminal decision.
package verdict

import "shieldtunnel/domain"

// Arbitrator is the stable interface for re-inspection and terminal decisions.
type Arbitrator interface {
	// AddPressureTrace appends one compartment pressure reading, validating the
	// time axis, integration and decay rate with fixed-point arithmetic.
	AddPressureTrace(req PressureTraceRequest) (domain.OperationReceipt, error)

	// PropagateRetest deterministically derives and stores the unique sorted
	// retest set from an anomaly source and creates a new generation.
	PropagateRetest(req RetestRequest) (domain.RetestCase, error)

	// SubmitReview records one independent qualified reviewer sign-off.
	SubmitReview(req ReviewRequest) (domain.Review, error)

	// Decide competes to submit the single terminal decision; only one of
	// admit/isolate/cancel may commit, others receive the existing terminal.
	Decide(req DecisionRequest) (domain.TerminalDecision, error)
}

// PressureTraceRequest appends a compartment pressure point.
type PressureTraceRequest struct {
	OperationID string               `json:"operation_id"`
	RingID      string               `json:"-"`
	Generation  domain.Generation    `json:"generation"`
	Trace       domain.PressureTrace `json:"trace"`
}

// RetestRequest triggers anomaly propagation from a source.
type RetestRequest struct {
	OperationID string            `json:"operation_id"`
	RingID      string            `json:"-"`
	Generation  domain.Generation `json:"generation"`
	Source      string            `json:"source"`
}

// ReviewRequest records a reviewer sign-off.
type ReviewRequest struct {
	OperationID string            `json:"operation_id"`
	RingID      string            `json:"-"`
	Generation  domain.Generation `json:"generation"`
	Reviewer    string            `json:"reviewer"`
	Qualified   bool              `json:"qualified"`
	Approved    bool              `json:"approved"`
}

// DecisionRequest submits one terminal decision.
type DecisionRequest struct {
	OperationID string            `json:"operation_id"`
	RingID      string            `json:"-"`
	Generation  domain.Generation `json:"generation"`
	Kind        string            `json:"kind"`
}

// VerdictError builds a single-reason verdict error.
func VerdictError(code domain.ErrorCode, msg string) *domain.Error {
	return &domain.Error{Code: code, Reasons: []domain.Reason{{Code: code, Message: msg}}}
}

// Package process implements the paste/assembly/tightening evidence recorder
// (粘贴拼装及紧固证据记录器). It enforces the dependency state machine from
// cleaning through staged bolt tightening, maintains the continuous paste and
// fastening prefixes, and records scripted device call results.
package process

import "shieldtunnel/domain"

// EvidenceRecorder is the stable interface for recording process evidence.
type EvidenceRecorder interface {
	// Record appends one process, bolt-stage or geometry evidence event,
	// enforcing the locked dependency chain and continuous prefix invariants.
	Record(req EvidenceRequest) (domain.OperationReceipt, error)

	// RecordDeviceAttempt appends a scripted device call. A fault produces a
	// pending retry record and never advances business state; only a valid
	// reading can later satisfy an evidence step.
	RecordDeviceAttempt(req DeviceAttemptRequest) (domain.DeviceAttempt, error)

	// Prefix returns the current continuous paste/fastening prefix length for
	// a ring generation.
	Prefix(ringID string, generation domain.Generation) (int, error)
}

// EvidenceRequest is the payload for one evidence submission.
type EvidenceRequest struct {
	OperationID string                    `json:"operation_id"`
	RingID      string                    `json:"-"`
	Generation  domain.Generation         `json:"generation"`
	LogicalTime int64                     `json:"logical_time"`
	Operator    string                    `json:"operator"` // lease holder identity
	Process     *domain.ProcessEvidence   `json:"process,omitempty"`
	Bolt        *domain.BoltStageEvidence `json:"bolt,omitempty"`
	Geometry    *domain.GeometryEvidence  `json:"geometry,omitempty"`
}

// DeviceAttemptRequest is the payload for a scripted device call.
type DeviceAttemptRequest struct {
	OperationID string            `json:"operation_id"`
	RingID      string            `json:"ring_id"`
	Generation  domain.Generation `json:"generation"`
	DeviceType  string            `json:"device_type"`
	CallNo      int               `json:"call_no"`
	LogicalTime int64             `json:"logical_time"`
	FaultCode   string            `json:"fault_code"`
	Reading     *domain.Fixed     `json:"reading,omitempty"`
}

// ProcessError builds a single-reason process error.
func ProcessError(code domain.ErrorCode, msg string) *domain.Error {
	return &domain.Error{Code: code, Reasons: []domain.Reason{{Code: code, Message: msg}}}
}

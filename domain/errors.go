package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ErrorCode is a stable machine-readable domain error code.
type ErrorCode string

// Stable domain error codes, grouped by failure boundary.
const (
	CodeOK                  ErrorCode = "ok"
	CodeSectionMismatch     ErrorCode = "section_mismatch"
	CodeRingTypeMismatch    ErrorCode = "ring_type_mismatch"
	CodeStaleSummary        ErrorCode = "stale_summary"
	CodeDuplicateSegment    ErrorCode = "duplicate_segment"
	CodeBadWedge            ErrorCode = "bad_wedge_direction"
	CodeAngleSumMismatch    ErrorCode = "angle_sum_mismatch"
	CodeMissingEdge         ErrorCode = "missing_edge"
	CodeDuplicatePairing    ErrorCode = "duplicate_pairing"
	CodeNonUniqueClosure    ErrorCode = "non_unique_closure"
	CodeDegenerateGeometry  ErrorCode = "degenerate_geometry"
	CodeOverflow            ErrorCode = "arithmetic_overflow"
	CodeDivideByZero        ErrorCode = "divide_by_zero"
	CodeNegativeTime        ErrorCode = "negative_time"
	CodeMaterialUnbalanced  ErrorCode = "material_unbalanced"
	CodeDuplicateBinding    ErrorCode = "duplicate_binding"
	CodeLeaseExpired        ErrorCode = "lease_expired"
	CodeLeaseHolderMismatch ErrorCode = "lease_holder_mismatch"
	CodeLogicalTimeOrder    ErrorCode = "logical_time_order"
	CodeStaleGeneration     ErrorCode = "stale_generation"
	CodeIdempotentConflict  ErrorCode = "idempotent_conflict"
	CodePasteGap            ErrorCode = "paste_gap"
	CodeOpenTimeExpired     ErrorCode = "open_time_expired"
	CodeDeviceFault         ErrorCode = "device_fault"
	CodeRetryExceeded       ErrorCode = "retry_exceeded"
	CodeNotQualified        ErrorCode = "not_qualified"
	CodeDuplicateReviewer   ErrorCode = "duplicate_reviewer"
	CodeRetestOpen          ErrorCode = "retest_open"
	CodeTerminalExists      ErrorCode = "terminal_exists"
	CodeNotFound            ErrorCode = "not_found"
	CodeOutOfTolerance      ErrorCode = "out_of_tolerance"
	CodeInternal            ErrorCode = "internal"
)

// Reason is a single deterministic, sortable failure cause.
type Reason struct {
	Code        ErrorCode  `json:"code"`
	Section     Section    `json:"section,omitempty"`
	RingNo      RingNo     `json:"ring_no,omitempty"`
	SegmentSeq  int        `json:"segment_seq,omitempty"`
	JointType   JointType  `json:"joint_type,omitempty"`
	SealSection int        `json:"seal_section,omitempty"`
	Generation  Generation `json:"generation,omitempty"`
	Message     string     `json:"message,omitempty"`
}

// sortKey returns the stable multi-key ordering value for a reason.
func (r Reason) sortKey() string {
	return fmt.Sprintf("%s|%d|%d|%s|%d|%d|%s",
		r.Section, r.RingNo, r.SegmentSeq, r.JointType, r.SealSection, r.Generation, r.Code)
}

// SortReasons returns a copy of rs ordered by the documented stable key:
// section, ring, segment seq, joint type, seal section, generation, code.
// The result is always non-nil so an empty list serializes as [] not null.
func SortReasons(rs []Reason) []Reason {
	out := make([]Reason, len(rs))
	copy(out, rs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].sortKey() < out[j].sortKey() })
	return out
}

// Error is a domain error carrying a primary code and sorted reasons.
type Error struct {
	Code      ErrorCode
	Operation string
	Reasons   []Reason
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Operation != "" {
		b.WriteString(" op=" + e.Operation)
	}
	if len(e.Reasons) > 0 {
		b.WriteString(" reasons=")
		for i, r := range e.Reasons {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(string(r.Code))
		}
	}
	return b.String()
}

// Envelope is the stable JSON error/response envelope.
type Envelope struct {
	Code        ErrorCode `json:"code"`
	OperationID string    `json:"operation_id"`
	Reasons     []Reason  `json:"reasons"`
}

// NewEnvelope builds an envelope with reasons already deterministically sorted.
func NewEnvelope(op string, code ErrorCode, reasons ...Reason) Envelope {
	return Envelope{Code: code, OperationID: op, Reasons: SortReasons(reasons)}
}

// AsError converts an envelope into an *Error.
func (e Envelope) AsError() *Error {
	return &Error{Code: e.Code, Operation: e.OperationID, Reasons: e.Reasons}
}

// MarshalJSON renders the envelope deterministically.
func (e Envelope) MarshalJSON() ([]byte, error) {
	type alias Envelope
	return json.Marshal(alias(e))
}

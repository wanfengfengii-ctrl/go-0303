// Package ring implements the ring-task and joint-closure aggregate
// (整环任务及接缝闭合聚合). It owns the append-only
// ring—segment—joint—seal-section graph, immutable task snapshots, segment
// positions, longitudinal/circumferential pairing and generations.
package ring

import "shieldtunnel/domain"

// RingAggregate is the stable interface for locking and querying ring tasks.
type RingAggregate interface {
	// Lock validates a lock request against the rule catalogue and produces an
	// immutable task snapshot. It rejects section/ring-type mismatch, stale
	// summaries, duplicate segments, bad wedge directions, angle-sum mismatch,
	// missing/multiple pairings, non-unique closure and degenerate geometry,
	// returning all reasons deterministically sorted.
	Lock(req LockRequest) (domain.RingTask, error)

	// Graph returns the current closed-loop graph for a section/ring, ordered
	// by segment seq, joint type and seal-section seq, plus the rule summary
	// and active generation.
	Graph(section domain.Section, ringNo domain.RingNo) (domain.RingTask, error)
}

// LockRequest is the immutable payload for locking a ring task (任务锁定).
type LockRequest struct {
	OperationID  string               `json:"operation_id"`
	Section      domain.Section       `json:"section"`
	RingNo       domain.RingNo        `json:"ring_no"`
	RingType     domain.RingType      `json:"ring_type"`
	Generation   domain.Generation    `json:"generation"`
	RuleSummary  domain.RuleSummary   `json:"rule_summary"`
	LogicalTime  int64                `json:"logical_time"`
	Segments     []domain.Segment     `json:"segments"`
	Joints       []domain.Joint       `json:"joints"`
	SealSections []domain.SealSection `json:"seal_sections"`
}

// ClosureError builds a single-reason error for closure validation failures.
func ClosureError(code domain.ErrorCode, msg string) *domain.Error {
	return &domain.Error{Code: code, Reasons: []domain.Reason{{Code: code, Message: msg}}}
}

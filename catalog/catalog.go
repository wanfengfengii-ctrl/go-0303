// Package catalog implements the segment-construction and seal-material rule
// catalogue (管片构造与密封材料规则目录). It owns ring types, integer
// geometry, joint templates, material/process specs, leak-bay adjacency and
// thresholds, and produces content-addressed rule summaries for locking.
package catalog

import "shieldtunnel/domain"

// Catalog is the stable interface for the rule catalogue component. A
// concrete implementation stores immutable rule versions and produces the
// RuleSummary that a ring lock request must carry to prove freshness.
type Catalog interface {
	// Snapshot returns the frozen RuleSnapshot for a section/ring-type pair,
	// or a sorted *domain.Error when the pair is incompatible or unknown.
	Snapshot(section domain.Section, ringType domain.RingType) (domain.RuleSnapshot, error)

	// Summarize returns the current content-addressed RuleSummary for the
	// given section and ring type.
	Summarize(section domain.Section, ringType domain.RingType) (domain.RuleSummary, error)

	// ValidateGeometry checks a segment's integer geometry against the
	// catalogued template and rejects degenerate or negative values.
	ValidateGeometry(t domain.SegmentTemplate, g domain.GrooveGeometry, h domain.HoleGeometry) error

	// ListSummaries returns every known section/ring-type summary in a stable
	// order, surfaced to the frontend for task locking.
	ListSummaries() []SummaryEntry
}

// CatalogError builds a single-reason domain error with the given code.
func CatalogError(code domain.ErrorCode, msg string) *domain.Error {
	return &domain.Error{
		Code:    code,
		Reasons: []domain.Reason{{Code: code, Message: msg}},
	}
}

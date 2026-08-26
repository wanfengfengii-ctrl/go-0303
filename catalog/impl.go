package catalog

import (
	"sync"

	"shieldtunnel/domain"
)

// sectionKey identifies one catalogue entry.
type sectionKey struct {
	section  domain.Section
	ringType domain.RingType
}

// Static is the built-in, immutable rule catalogue. It owns ring types,
// integer geometry, joint templates, material/process specs, leak-bay
// adjacency and thresholds, and produces content-addressed rule summaries for
// locking. The catalogue is read-only after construction and safe for
// concurrent access.
type Static struct {
	mu    sync.RWMutex
	rules map[sectionKey]domain.RuleSnapshot
}

// NewStatic builds the catalogue of known sections and ring types.
func NewStatic() *Static {
	c := &Static{rules: make(map[sectionKey]domain.RuleSnapshot)}
	c.register(chengjiangRoad())
	c.register(wangtaStation())
	return c
}

func (c *Static) register(s domain.RuleSnapshot) {
	s.Summary = Summarize(s)
	c.rules[sectionKey{s.Section, s.RingType}] = s
}

// Snapshot returns the frozen RuleSnapshot for a section/ring-type pair.
func (c *Static) Snapshot(section domain.Section, ringType domain.RingType) (domain.RuleSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.rules[sectionKey{section, ringType}]
	if !ok {
		return domain.RuleSnapshot{}, &domain.Error{
			Code: domain.CodeRingTypeMismatch,
			Reasons: []domain.Reason{{
				Code:    domain.CodeRingTypeMismatch,
				Section: section,
				Message: "unknown ring type for section",
			}},
		}
	}
	return snap, nil
}

// Summarize returns the current RuleSummary for a section and ring type.
func (c *Static) Summarize(section domain.Section, ringType domain.RingType) (domain.RuleSummary, error) {
	snap, err := c.Snapshot(section, ringType)
	if err != nil {
		return "", err
	}
	return snap.Summary, nil
}

// SummaryEntry is one catalogue entry surfaced to the frontend so it can lock
// a real task against a fresh summary.
type SummaryEntry struct {
	Section  domain.Section     `json:"section"`
	RingType domain.RingType    `json:"ring_type"`
	Summary  domain.RuleSummary `json:"summary"`
}

// ListSummaries returns every known section/ring-type summary in a stable order.
func (c *Static) ListSummaries() []SummaryEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]sectionKey, 0, len(c.rules))
	for k := range c.rules {
		keys = append(keys, k)
	}
	// Stable ordering by section then ring type.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && (keys[j].section < keys[j-1].section ||
			(keys[j].section == keys[j-1].section && keys[j].ringType < keys[j-1].ringType)); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	out := make([]SummaryEntry, 0, len(keys))
	for _, k := range keys {
		s := c.rules[k]
		out = append(out, SummaryEntry{Section: k.section, RingType: k.ringType, Summary: s.Summary})
	}
	return out
}

// ValidateGeometry checks a segment's integer geometry against the catalogued
// template and rejects degenerate or negative values.
func (c *Static) ValidateGeometry(t domain.SegmentTemplate, g domain.GrooveGeometry, h domain.HoleGeometry) error {
	var reasons []domain.Reason
	if g.WidthMM <= 0 || g.DepthMM <= 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "groove width/depth must be positive"})
	}
	if g.CornerMM < 0 || g.JointPosMM < 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "corner/joint position must be non-negative"})
	}
	if h.Count < 0 || h.SpacingMM < 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "hole count/spacing must be non-negative"})
	}
	if t.CenterAngle <= 0 {
		reasons = append(reasons, domain.Reason{Code: domain.CodeDegenerateGeometry, Message: "center angle must be positive"})
	}
	if len(reasons) > 0 {
		return &domain.Error{Code: domain.CodeDegenerateGeometry, Reasons: reasons}
	}
	return nil
}

// chengjiangRoad is the catalogue entry used by the acceptance test scenario:
// section "澄江路—望塔站", universal wedge ring with 6 integer-degree segments.
func chengjiangRoad() domain.RuleSnapshot {
	return domain.RuleSnapshot{
		Section:        "澄江路—望塔站",
		RingType:       "通用楔形环",
		CenterAngleSum: 360,
		SegmentTemplate: []domain.SegmentTemplate{
			{Type: domain.SegmentKey, CenterAngle: 30},
			{Type: domain.SegmentAdj, CenterAngle: 60},
			{Type: domain.SegmentStd, CenterAngle: 70},
		},
		WedgeConstraint: domain.WedgeLeft,
		GrooveGeometry:  domain.GrooveGeometry{WidthMM: 12, DepthMM: 8, CornerMM: 4, JointPosMM: 20},
		HoleGeometry:    domain.HoleGeometry{Count: 12, SpacingMM: 60},
		JointTemplates: []domain.JointTemplate{
			{Type: domain.JointLongitudinal, PairingRule: "adjacent-segment"},
			{Type: domain.JointCircum, PairingRule: "previous-ring"},
		},
		MaterialSpec: domain.MaterialSpec{GasketBatch: "GASKET-2026A", AdhesiveBatch: "ADH-2026B"},
		ProcessSpec:  domain.ProcessSpec{PasteStart: 0, AssemblyOrder: []int{0, 1, 2, 3, 4, 5}, BoltStages: []int{1, 2, 3}},
		LeakBayGraph: [][]int{{1, 2}, {0, 2, 3}, {0, 1, 3}, {1, 2}},
		Thresholds: domain.Thresholds{
			ElongationMax:    100000, // 0.10
			LapRateMin:       30000,  // 0.03
			GluePerLengthMax: 500000, // 0.5 mg/mm
			OpenTimeMax:      600,
			OpeningMax:       1000000, // 1.0 mm
			OffsetMax:        2000000, // 2.0 mm
			CompressionMin:   200000,  // 0.2
			PreloadDevMax:    150000,  // 0.15
			DecayRateMax:     50000,   // 0.05 per unit time
			RetryLimit:       3,
		},
	}
}

// wangtaStation is a second catalogue entry used to exercise section/ring-type
// mismatch and summary-staleness failures.
func wangtaStation() domain.RuleSnapshot {
	s := chengjiangRoad()
	s.Section = "望塔站—江心洲"
	s.RingType = "标准环"
	s.WedgeConstraint = domain.WedgeNone
	s.SegmentTemplate = []domain.SegmentTemplate{
		{Type: domain.SegmentKey, CenterAngle: 40},
		{Type: domain.SegmentAdj, CenterAngle: 80},
		{Type: domain.SegmentStd, CenterAngle: 80},
	}
	s.MaterialSpec = domain.MaterialSpec{GasketBatch: "GASKET-STD", AdhesiveBatch: "ADH-STD"}
	return s
}

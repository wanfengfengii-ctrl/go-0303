package ring

import (
	"shieldtunnel/domain"
)

// templateAngle returns the catalogued integer center angle for a segment type.
func templateAngle(snap domain.RuleSnapshot, t domain.SegmentType) (int64, bool) {
	for _, tmpl := range snap.SegmentTemplate {
		if tmpl.Type == t {
			return tmpl.CenterAngle, true
		}
	}
	return 0, false
}

// closureReasons performs the pure topology and identity validation for a lock
// request against a frozen rule snapshot. It collects every failure cause so a
// single request can report all reasons deterministically sorted. Geometry is
// validated separately against the catalogue.
func closureReasons(snap domain.RuleSnapshot, req LockRequest) []domain.Reason {
	var reasons []domain.Reason

	// Stale summary: the lock must reference the current catalogue digest.
	if req.RuleSummary != snap.Summary {
		reasons = append(reasons, domain.Reason{
			Code: domain.CodeStaleSummary, Section: req.Section, RingNo: req.RingNo,
			Message: "rule summary is stale",
		})
	}

	// Duplicate segment identities and angle-sum/type mismatches.
	seen := make(map[int]bool, len(req.Segments))
	var angleSum int64
	for _, s := range req.Segments {
		if seen[s.Seq] {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeDuplicateSegment, Section: req.Section, RingNo: req.RingNo,
				SegmentSeq: s.Seq, Message: "duplicate segment seq",
			})
			continue
		}
		seen[s.Seq] = true
		angleSum += s.CenterAngle
		if want, ok := templateAngle(snap, s.Type); !ok {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeRingTypeMismatch, Section: req.Section, RingNo: req.RingNo,
				SegmentSeq: s.Seq, Message: "unknown segment type",
			})
		} else if s.CenterAngle != want {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeDegenerateGeometry, Section: req.Section, RingNo: req.RingNo,
				SegmentSeq: s.Seq, Message: "segment center angle does not match template",
			})
		}
	}

	// Integer angle-sum must equal the catalogue value.
	if angleSum != snap.CenterAngleSum {
		reasons = append(reasons, domain.Reason{
			Code: domain.CodeAngleSumMismatch, Section: req.Section, RingNo: req.RingNo,
			Message: "segment center angle sum does not match ring rule",
		})
	}

	// Wedge direction validity.
	for _, s := range req.Segments {
		switch s.Wedge {
		case domain.WedgeLeft, domain.WedgeRight, domain.WedgeNone:
		default:
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeBadWedge, Section: req.Section, RingNo: req.RingNo,
				SegmentSeq: s.Seq, Message: "invalid wedge direction",
			})
		}
	}

	reasons = append(reasons, topologyReasons(req.Section, req.RingNo, req.Segments, req.Joints)...)
	return reasons
}

// topologyReasons validates that the longitudinal joints form a single simple
// cycle covering every segment and that every segment has exactly one
// circumferential joint. Longitudinal joints pair two distinct segments;
// circumferential joints are self-pairings marking the front/back seam.
func topologyReasons(section domain.Section, ringNo domain.RingNo, segments []domain.Segment, joints []domain.Joint) []domain.Reason {
	var reasons []domain.Reason
	n := len(segments)
	if n == 0 {
		return []domain.Reason{{Code: domain.CodeMissingEdge, Section: section, RingNo: ringNo, Message: "no segments"}}
	}

	degree := make(map[int]int, n)
	circ := make(map[int]int, n)
	edgeSeen := make(map[edge]bool)

	for _, j := range joints {
		if j.Type == domain.JointLongitudinal {
			a, b := j.EdgeA.SegmentSeq, j.EdgeB.SegmentSeq
			if a == b {
				reasons = append(reasons, domain.Reason{
					Code: domain.CodeDuplicatePairing, Section: section, RingNo: ringNo,
					JointType: j.Type, Message: "longitudinal joint cannot pair a segment to itself",
				})
				continue
			}
			if a > b {
				a, b = b, a
			}
			if edgeSeen[edge{a, b}] {
				reasons = append(reasons, domain.Reason{
					Code: domain.CodeDuplicatePairing, Section: section, RingNo: ringNo,
					JointType: j.Type, Message: "duplicate longitudinal pairing",
				})
				continue
			}
			edgeSeen[edge{a, b}] = true
			degree[a]++
			degree[b]++
		} else if j.Type == domain.JointCircum {
			circ[j.EdgeA.SegmentSeq]++
		}
	}

	for _, s := range segments {
		if degree[s.Seq] < 2 {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeMissingEdge, Section: section, RingNo: ringNo,
				SegmentSeq: s.Seq, Message: "segment missing a longitudinal edge",
			})
		} else if degree[s.Seq] > 2 {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeDuplicatePairing, Section: section, RingNo: ringNo,
				SegmentSeq: s.Seq, Message: "segment has too many longitudinal pairings",
			})
		}
		if circ[s.Seq] != 1 {
			reasons = append(reasons, domain.Reason{
				Code: domain.CodeMissingEdge, Section: section, RingNo: ringNo,
				SegmentSeq: s.Seq, Message: "segment missing circumferential joint",
			})
		}
	}

	// Single connected component => single simple cycle.
	if !connected(segments, edgeSeen) {
		reasons = append(reasons, domain.Reason{
			Code: domain.CodeNonUniqueClosure, Section: section, RingNo: ringNo,
			Message: "segments do not form a single simple closure",
		})
	}
	return reasons
}

// edge is an undirected longitudinal pairing between two segment sequences.
type edge struct{ a, b int }

// connected reports whether the longitudinal edge set connects every segment.
func connected(segments []domain.Segment, edges map[edge]bool) bool {
	if len(segments) == 0 {
		return false
	}
	adj := make(map[int][]int)
	for e := range edges {
		adj[e.a] = append(adj[e.a], e.b)
		adj[e.b] = append(adj[e.b], e.a)
	}
	start := segments[0].Seq
	visited := map[int]bool{start: true}
	queue := []int{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	for _, s := range segments {
		if !visited[s.Seq] {
			return false
		}
	}
	return true
}

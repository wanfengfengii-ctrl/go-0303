package ring

import (
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/store"
)

const (
	testSection  = domain.Section("澄江路—望塔站")
	testRingType = domain.RingType("通用楔形环")
)

func newAggregate(t *testing.T) *Aggregate {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewAggregate(catalog.NewStatic(), db)
}

func validSegments() []domain.Segment {
	g := domain.GrooveGeometry{WidthMM: 12, DepthMM: 8, CornerMM: 4, JointPosMM: 20}
	h := domain.HoleGeometry{Count: 12, SpacingMM: 60}
	return []domain.Segment{
		{Seq: 0, Type: domain.SegmentKey, CenterAngle: 30, Wedge: domain.WedgeNone, Groove: g, Holes: h},
		{Seq: 1, Type: domain.SegmentAdj, CenterAngle: 60, Wedge: domain.WedgeLeft, Groove: g, Holes: h},
		{Seq: 2, Type: domain.SegmentAdj, CenterAngle: 60, Wedge: domain.WedgeLeft, Groove: g, Holes: h},
		{Seq: 3, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: h},
		{Seq: 4, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: h},
		{Seq: 5, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: h},
	}
}

func validJoints() []domain.Joint {
	joints := make([]domain.Joint, 0, 12)
	for i := 0; i < 6; i++ {
		joints = append(joints, domain.Joint{
			Type:  domain.JointLongitudinal,
			EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "right"},
			EdgeB: domain.SegmentEdge{SegmentSeq: (i + 1) % 6, Side: "left"},
		})
		joints = append(joints, domain.Joint{
			Type:  domain.JointCircum,
			EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "front"},
			EdgeB: domain.SegmentEdge{SegmentSeq: i, Side: "back"},
		})
	}
	return joints
}

func validLockRequest(t *testing.T, agg *Aggregate) LockRequest {
	t.Helper()
	sum, err := agg.catalog.Summarize(testSection, testRingType)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	return LockRequest{
		OperationID: "lock-1",
		Section:     testSection,
		RingNo:      3,
		RingType:    testRingType,
		Generation:  1,
		RuleSummary: sum,
		LogicalTime: 0,
		Segments:    validSegments(),
		Joints:      validJoints(),
	}
}

func TestLockValidRingFormsUniqueClosure(t *testing.T) {
	agg := newAggregate(t)
	task, err := agg.Lock(validLockRequest(t, agg))
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if len(task.Segments) != 6 {
		t.Fatalf("segments %d want 6", len(task.Segments))
	}
	// Graph returns the same task.
	got, err := agg.Graph(testSection, 3)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if got.ID != task.ID {
		t.Fatalf("graph id %s want %s", got.ID, task.ID)
	}
}

func TestLockRejectsStaleSummary(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	req.RuleSummary = "stale"
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeStaleSummary)
}

func TestLockRejectsDuplicateSegment(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	req.Segments[1].Seq = 0
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeDuplicateSegment)
}

func TestLockRejectsAngleSumMismatch(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	req.Segments[0].CenterAngle = 40
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeAngleSumMismatch)
}

func TestLockRejectsBadWedgeDirection(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	req.Segments[2].Wedge = "sideways"
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeBadWedge)
}

func TestLockRejectsMissingEdge(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	// Drop the longitudinal joint 1-2 (index 2*1 in the slice).
	req.Joints = append(req.Joints[:2], req.Joints[3:]...)
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeMissingEdge)
}

func TestLockRejectsDuplicatePairing(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	// Duplicate the 0-1 longitudinal joint.
	req.Joints = append(req.Joints, domain.Joint{
		Type:  domain.JointLongitudinal,
		EdgeA: domain.SegmentEdge{SegmentSeq: 0, Side: "right"},
		EdgeB: domain.SegmentEdge{SegmentSeq: 1, Side: "left"},
	})
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeDuplicatePairing)
}

func TestLockRejectsDegenerateGeometry(t *testing.T) {
	agg := newAggregate(t)
	req := validLockRequest(t, agg)
	req.Segments[0].Groove.WidthMM = 0
	_, err := agg.Lock(req)
	assertCode(t, err, domain.CodeDegenerateGeometry)
}

func assertCode(t *testing.T, err error, want domain.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	de, ok := err.(*domain.Error)
	if !ok {
		t.Fatalf("expected *domain.Error, got %T", err)
	}
	for _, r := range de.Reasons {
		if r.Code == want {
			return
		}
	}
	t.Fatalf("reasons %v missing code %s", de.Reasons, want)
}

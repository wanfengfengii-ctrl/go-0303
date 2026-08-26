package verdict

import (
	"sync"
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/ring"
	"shieldtunnel/store"
)

type harness struct {
	db    *store.DB
	arb   *Arbiter
	rings *ring.Aggregate
	id    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cat := catalog.NewStatic()
	h := &harness{db: db, arb: NewArbiter(db), rings: ring.NewAggregate(cat, db)}
	h.id = h.lock(t)
	return h
}

func (h *harness) lock(t *testing.T) string {
	t.Helper()
	sum, _ := catalog.NewStatic().Summarize(domain.Section("澄江路—望塔站"), domain.RingType("通用楔形环"))
	g := domain.GrooveGeometry{WidthMM: 12, DepthMM: 8, CornerMM: 4, JointPosMM: 20}
	hole := domain.HoleGeometry{Count: 12, SpacingMM: 60}
	var segs []domain.Segment
	for i, typ := range []domain.SegmentType{domain.SegmentKey, domain.SegmentAdj, domain.SegmentAdj, domain.SegmentStd, domain.SegmentStd, domain.SegmentStd} {
		ang := []int64{30, 60, 60, 70, 70, 70}[i]
		segs = append(segs, domain.Segment{Seq: i, Type: typ, CenterAngle: ang, Wedge: domain.WedgeLeft, Groove: g, Holes: hole})
	}
	var joints []domain.Joint
	for i := 0; i < 6; i++ {
		joints = append(joints,
			domain.Joint{Type: domain.JointLongitudinal, EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "right"}, EdgeB: domain.SegmentEdge{SegmentSeq: (i + 1) % 6, Side: "left"}},
			domain.Joint{Type: domain.JointCircum, EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "front"}, EdgeB: domain.SegmentEdge{SegmentSeq: i, Side: "back"}},
		)
	}
	task, err := h.rings.Lock(ring.LockRequest{
		OperationID: "lock", Section: "澄江路—望塔站", RingNo: 1, RingType: "通用楔形环",
		Generation: 1, RuleSummary: sum, LogicalTime: 0, Segments: segs, Joints: joints,
	})
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	return task.ID
}

func testTask() domain.RingTask {
	var segs []domain.Segment
	for i := 0; i < 6; i++ {
		segs = append(segs, domain.Segment{Seq: i})
	}
	return domain.RingTask{Segments: segs}
}

func TestPropagateJointCrackAdjacentJoints(t *testing.T) {
	got := propagate(testTask(), nil, "joint_crack:2")
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("affected %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("affected %v want %v", got, want)
		}
	}
}

func TestPropagateSharedGasketBatch(t *testing.T) {
	// Segments 0 and 4 share gasket batch "B1".
	bindings := []store.GasketBinding{
		{BarID: "b1", Batch: "B1", SlotSeq: 0},
		{BarID: "b2", Batch: "B1", SlotSeq: 4},
		{BarID: "b3", Batch: "B2", SlotSeq: 1},
	}
	got := propagate(testTask(), bindings, "joint_crack:0")
	// Segment 0 -> neighbours 5,1; shared batch B1 pulls in 4.
	for _, want := range []int{0, 1, 4, 5} {
		if !contains(got, want) {
			t.Fatalf("affected %v missing %d", got, want)
		}
	}
}

func TestDecayRateBoundary(t *testing.T) {
	ok := []domain.PressureTrace{
		{Bay: 0, LogicalTime: 0, Pressure: 1000000},
		{Bay: 0, LogicalTime: 100, Pressure: 990000}, // drop 10000 over 100 -> 100/unit
	}
	rate, err := decayRate(ok)
	if err != nil {
		t.Fatalf("decay: %v", err)
	}
	if rate != 100 {
		t.Fatalf("decay %d want 100", rate)
	}
}

func TestPressureTraceRejectsNegativeTime(t *testing.T) {
	h := newHarness(t)
	_, err := h.arb.AddPressureTrace(PressureTraceRequest{
		OperationID: "t1", RingID: h.id, Generation: 1,
		Trace: domain.PressureTrace{Bay: 0, LogicalTime: -1, Pressure: 1000000},
	})
	if err == nil {
		t.Fatal("expected negative time rejection")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeNegativeTime {
		t.Fatalf("code %s", de.Code)
	}
}

func TestPressureTraceRejectsTimeRegression(t *testing.T) {
	h := newHarness(t)
	h.arb.AddPressureTrace(PressureTraceRequest{OperationID: "t1", RingID: h.id, Generation: 1, Trace: domain.PressureTrace{Bay: 0, LogicalTime: 10, Pressure: 1000000}})
	_, err := h.arb.AddPressureTrace(PressureTraceRequest{OperationID: "t2", RingID: h.id, Generation: 1, Trace: domain.PressureTrace{Bay: 0, LogicalTime: 5, Pressure: 1000000}})
	if err == nil {
		t.Fatal("expected time regression rejection")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeLogicalTimeOrder {
		t.Fatalf("code %s", de.Code)
	}
}

func TestTerminalDuplicateReviewer(t *testing.T) {
	h := newHarness(t)
	h.review(t, "r1", true, true)
	_, err := h.arb.SubmitReview(ReviewRequest{OperationID: "r1b", RingID: h.id, Generation: 1, Reviewer: "r1", Qualified: true, Approved: true})
	if err == nil {
		t.Fatal("expected duplicate reviewer")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeDuplicateReviewer {
		t.Fatalf("code %s", de.Code)
	}
}

func TestTerminalNotQualified(t *testing.T) {
	h := newHarness(t)
	h.review(t, "r1", true, true)
	h.review(t, "r2", false, true) // not qualified
	_, err := h.arb.Decide(DecisionRequest{OperationID: "d1", RingID: h.id, Generation: 1, Kind: "admit"})
	if err == nil {
		t.Fatal("expected not qualified")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeNotQualified {
		t.Fatalf("code %s", de.Code)
	}
}

func TestTerminalRetestOpen(t *testing.T) {
	h := newHarness(t)
	if _, err := h.arb.PropagateRetest(RetestRequest{OperationID: "p1", RingID: h.id, Generation: 1, Source: "joint_crack:2"}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	// Ring generation bumped to 2; retest unresolved -> open.
	_, err := h.arb.Decide(DecisionRequest{OperationID: "d1", RingID: h.id, Generation: 2, Kind: "admit"})
	if err == nil {
		t.Fatal("expected retest open")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeRetestOpen {
		t.Fatalf("code %s", de.Code)
	}
	// After two qualified approvers for generation 2, the retest is closed.
	h.reviewGen(t, 2, "r1", true, true)
	h.reviewGen(t, 2, "r2", true, true)
	if _, err := h.arb.Decide(DecisionRequest{OperationID: "d2", RingID: h.id, Generation: 2, Kind: "admit"}); err != nil {
		t.Fatalf("admit after retest closed: %v", err)
	}
}

func TestTerminalConcurrentSingleWinner(t *testing.T) {
	h := newHarness(t)
	h.review(t, "r1", true, true)
	h.review(t, "r2", true, true)

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]domain.TerminalDecision, 3)
	errs := make([]error, 3)
	kinds := []string{"admit", "isolate", "cancel"}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			results[n], errs[n] = h.arb.Decide(DecisionRequest{
				OperationID: "d" + string(rune('a'+n)), RingID: h.id, Generation: 1, Kind: kinds[n],
			})
		}(i)
	}
	close(start)
	wg.Wait()

	first := results[0]
	for i := 1; i < 3; i++ {
		if errs[i] != nil && errs[0] == nil {
			t.Fatalf("loser %d errored while winner succeeded: %v", i, errs[i])
		}
		if results[i].Kind != first.Kind {
			t.Fatalf("terminal %d kind %s want %s", i, results[i].Kind, first.Kind)
		}
	}
}

func (h *harness) review(t *testing.T, reviewer string, qualified, approved bool) {
	t.Helper()
	h.reviewGen(t, 1, reviewer, qualified, approved)
}

func (h *harness) reviewGen(t *testing.T, gen domain.Generation, reviewer string, qualified, approved bool) {
	t.Helper()
	if _, err := h.arb.SubmitReview(ReviewRequest{
		OperationID: "rev-" + reviewer, RingID: h.id, Generation: gen, Reviewer: reviewer, Qualified: qualified, Approved: approved,
	}); err != nil {
		t.Fatalf("review: %v", err)
	}
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

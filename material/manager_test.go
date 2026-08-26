package material

import (
	"context"
	"sync"
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/ring"
	"shieldtunnel/store"
)

type harness struct {
	db    *store.DB
	rings *ring.Aggregate
	mgr   *Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cat := catalog.NewStatic()
	return &harness{db: db, rings: ring.NewAggregate(cat, db), mgr: NewManager(db)}
}

func (h *harness) lockRing(t *testing.T) string {
	t.Helper()
	sum, _ := catalog.NewStatic().Summarize(domain.Section("澄江路—望塔站"), domain.RingType("通用楔形环"))
	g := domain.GrooveGeometry{WidthMM: 12, DepthMM: 8, CornerMM: 4, JointPosMM: 20}
	hole := domain.HoleGeometry{Count: 12, SpacingMM: 60}
	segs := []domain.Segment{
		{Seq: 0, Type: domain.SegmentKey, CenterAngle: 30, Wedge: domain.WedgeNone, Groove: g, Holes: hole},
		{Seq: 1, Type: domain.SegmentAdj, CenterAngle: 60, Wedge: domain.WedgeLeft, Groove: g, Holes: hole},
		{Seq: 2, Type: domain.SegmentAdj, CenterAngle: 60, Wedge: domain.WedgeLeft, Groove: g, Holes: hole},
		{Seq: 3, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: hole},
		{Seq: 4, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: hole},
		{Seq: 5, Type: domain.SegmentStd, CenterAngle: 70, Wedge: domain.WedgeLeft, Groove: g, Holes: hole},
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

func balancedAllocate(ringID, op, barID string) AllocateRequest {
	return AllocateRequest{
		OperationID: op, RingID: ringID, Generation: 1, LogicalTime: 1,
		Slot:      domain.SegmentSlot{SegmentSeq: 0},
		GasketBar: domain.GasketBar{ID: barID, Batch: "GASKET-2026A", TotalLengthMM: 1000},
		Allocations: []domain.GasketAllocation{
			{BarID: barID, Kind: "valid", LengthMM: 800},
			{BarID: barID, Kind: "lap", LengthMM: 100},
			{BarID: barID, Kind: "sample", LengthMM: 50},
			{BarID: barID, Kind: "remainder", LengthMM: 30},
			{BarID: barID, Kind: "loss", LengthMM: 20},
		},
		AdhesiveIssue: domain.AdhesiveIssue{Batch: "ADH-2026B", Generation: 1, TotalMg: 1000, AppliedMg: 700, RetainedMg: 100, RecoveredMg: 100, LossMg: 100},
	}
}

func TestAllocateConservesBothLedgers(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	res, err := h.mgr.Allocate(balancedAllocate(id, "op-1", "bar-1"))
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if res.BarID != "bar-1" {
		t.Fatalf("bar %s", res.BarID)
	}
}

func TestAllocateRejectsGasketImbalance(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	req := balancedAllocate(id, "op-1", "bar-1")
	req.Allocations[0].LengthMM = 700 // sum 900 != 1000
	if _, err := h.mgr.Allocate(req); err == nil {
		t.Fatal("expected gasket imbalance error")
	}
	// The bar must not be bound.
	var bar *domain.GasketBar
	_ = h.db.WithTx(context.Background(), func(tx *store.Tx) error {
		b, err := tx.FindGasketBar(context.Background(), "bar-1")
		bar = b
		return err
	})
	if bar != nil {
		t.Fatalf("bar should not be bound after rejected allocation: %+v", bar)
	}
}

func TestAdhesiveImbalanceRollsBackBinding(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	req := balancedAllocate(id, "op-1", "bar-1")
	req.AdhesiveIssue.LossMg = 50 // total 1000 != 950
	if _, err := h.mgr.Allocate(req); err == nil {
		t.Fatal("expected adhesive imbalance error")
	}
	// Same bar is still allocatable afterwards.
	if _, err := h.mgr.Allocate(balancedAllocate(id, "op-2", "bar-1")); err != nil {
		t.Fatalf("bar should still be allocatable: %v", err)
	}
}

func TestConcurrentAllocationSingleWinner(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			op := "op-a"
			if n == 1 {
				op = "op-b"
			}
			_, results[n] = h.mgr.Allocate(balancedAllocate(id, op, "bar-shared"))
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes %d want 1 (results: %v)", successes, results)
	}
}

func TestAllocateIdempotentSameContent(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	first, err := h.mgr.Allocate(balancedAllocate(id, "op-1", "bar-1"))
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	second, err := h.mgr.Allocate(balancedAllocate(id, "op-1", "bar-1"))
	if err != nil {
		t.Fatalf("idempotent allocate: %v", err)
	}
	if second.BarID != first.BarID {
		t.Fatalf("idempotent result %s want %s", second.BarID, first.BarID)
	}
}

func TestAllocateIdempotentConflict(t *testing.T) {
	h := newHarness(t)
	id := h.lockRing(t)
	if _, err := h.mgr.Allocate(balancedAllocate(id, "op-1", "bar-1")); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	req := balancedAllocate(id, "op-1", "bar-other")
	if _, err := h.mgr.Allocate(req); err == nil {
		t.Fatal("expected idempotent conflict")
	} else if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeIdempotentConflict {
		t.Fatalf("expected idempotent conflict, got %v", err)
	}
}

func TestLeaseCompetitionAndExpiry(t *testing.T) {
	h := newHarness(t)
	// First holder acquires [0,100).
	if _, err := h.mgr.AcquireLease(LeaseRequest{OperationID: "l1", Resource: domain.ResourceGlueTable, ResourceID: "ring-1", Holder: "op1", Start: 0, End: 100}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Second holder cannot take over an active lease.
	if _, err := h.mgr.AcquireLease(LeaseRequest{OperationID: "l2", Resource: domain.ResourceGlueTable, ResourceID: "ring-1", Holder: "op2", Start: 50, End: 100}); err == nil {
		t.Fatal("expected holder mismatch")
	}
	// After expiry (Start >= End) a competitor can take over.
	if _, err := h.mgr.AcquireLease(LeaseRequest{OperationID: "l3", Resource: domain.ResourceGlueTable, ResourceID: "ring-1", Holder: "op2", Start: 100, End: 200}); err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	// Old holder late submission is rejected.
	if _, err := h.mgr.AcquireLease(LeaseRequest{OperationID: "l4", Resource: domain.ResourceGlueTable, ResourceID: "ring-1", Holder: "op1", Start: 150, End: 300}); err == nil {
		t.Fatal("expected old holder late submission rejection")
	}
}

func TestRenewLeaseRequiresCurrentHolder(t *testing.T) {
	h := newHarness(t)
	if _, err := h.mgr.AcquireLease(LeaseRequest{OperationID: "l1", Resource: domain.ResourceRoller, ResourceID: "ring-1", Holder: "op1", Start: 0, End: 100}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := h.mgr.RenewLease(LeaseRequest{OperationID: "l2", Resource: domain.ResourceRoller, ResourceID: "ring-1", Holder: "op2", Start: 50, End: 200}); err == nil {
		t.Fatal("expected renew holder mismatch")
	}
	if _, err := h.mgr.RenewLease(LeaseRequest{OperationID: "l3", Resource: domain.ResourceRoller, ResourceID: "ring-1", Holder: "op1", Start: 0, End: 200}); err != nil {
		t.Fatalf("renew by holder: %v", err)
	}
}

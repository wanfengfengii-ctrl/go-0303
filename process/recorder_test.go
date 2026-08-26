package process

import (
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/ring"
	"shieldtunnel/store"
)

type harness struct {
	db    *store.DB
	rings *ring.Aggregate
	mgr   *material.Manager
	rec   *Recorder
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
	h := &harness{db: db, rings: ring.NewAggregate(cat, db), mgr: material.NewManager(db), rec: NewRecorder(db)}
	h.id = h.lock(t)
	h.leases(t)
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

func (h *harness) leases(t *testing.T) {
	t.Helper()
	for _, res := range []domain.ResourceKind{domain.ResourceCleanBay, domain.ResourceGlueTable, domain.ResourceRoller, domain.ResourceErector, domain.ResourceTorqueTool} {
		if _, err := h.mgr.AcquireLease(material.LeaseRequest{
			OperationID: "lease-" + string(res), Resource: res, ResourceID: h.id, Holder: "op1", Start: 0, End: 10000,
		}); err != nil {
			t.Fatalf("lease %s: %v", res, err)
		}
	}
}

func (h *harness) process(kind string, at int64) EvidenceRequest {
	return EvidenceRequest{OperationID: kind + "-op", RingID: h.id, Generation: 1, LogicalTime: at, Operator: "op1",
		Process: &domain.ProcessEvidence{Kind: kind, Generation: 1, LogicalTime: at, InstrumentID: "inst-1"}}
}

func TestProcessPrefixGrowsInOrder(t *testing.T) {
	h := newHarness(t)
	order := []string{"clean", "dry", "cut", "joint", "glue", "paste", "roll", "cure", "seat"}
	at := int64(0)
	for i, kind := range order {
		at = int64((i + 1) * 10)
		if _, err := h.rec.Record(h.process(kind, at)); err != nil {
			t.Fatalf("record %s: %v", kind, err)
		}
	}
	p, err := h.rec.Prefix(h.id, 1)
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if p != 9 {
		t.Fatalf("prefix %d want 9", p)
	}
}

func TestProcessRejectsSkip(t *testing.T) {
	h := newHarness(t)
	if _, err := h.rec.Record(h.process("clean", 10)); err != nil {
		t.Fatalf("clean: %v", err)
	}
	_, err := h.rec.Record(h.process("paste", 20))
	if err == nil {
		t.Fatal("expected skip rejection")
	}
	de := err.(*domain.Error)
	if de.Code != domain.CodePasteGap {
		t.Fatalf("code %s want paste_gap", de.Code)
	}
}

func TestProcessRejectsStaleGeneration(t *testing.T) {
	h := newHarness(t)
	req := h.process("clean", 10)
	req.Generation = 2
	_, err := h.rec.Record(req)
	if err == nil {
		t.Fatal("expected stale generation")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeStaleGeneration {
		t.Fatalf("code %s", de.Code)
	}
}

func TestProcessRejectsOpenTimeExpired(t *testing.T) {
	h := newHarness(t)
	steps := []struct {
		kind string
		at   int64
	}{{"clean", 10}, {"dry", 20}, {"cut", 30}, {"joint", 40}, {"glue", 50}}
	for _, s := range steps {
		if _, err := h.rec.Record(h.process(s.kind, s.at)); err != nil {
			t.Fatalf("%s: %v", s.kind, err)
		}
	}
	// paste after OpenTimeMax (600) from glue -> expired.
	_, err := h.rec.Record(h.process("paste", 650))
	if err == nil {
		t.Fatal("expected open time expiry")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeOpenTimeExpired {
		t.Fatalf("code %s", de.Code)
	}
}

func TestProcessRejectsMissingLease(t *testing.T) {
	h := newHarness(t)
	// A different operator without a lease.
	req := h.process("clean", 10)
	req.Operator = "op2"
	_, err := h.rec.Record(req)
	if err == nil {
		t.Fatal("expected lease rejection")
	}
	if de := err.(*domain.Error); de.Code != domain.CodeLeaseExpired {
		t.Fatalf("code %s", de.Code)
	}
}

func TestBoltStagesInOrder(t *testing.T) {
	h := newHarness(t)
	order := []string{"clean", "dry", "cut", "joint", "glue", "paste", "roll", "cure", "seat"}
	for i, kind := range order {
		if _, err := h.rec.Record(h.process(kind, int64((i+1)*10))); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	// Bolt stage 2 out of order.
	bolt2 := EvidenceRequest{OperationID: "b2", RingID: h.id, Generation: 1, LogicalTime: 100, Operator: "op1",
		Bolt: &domain.BoltStageEvidence{Stage: 2, Generation: 1, LogicalTime: 100, PreloadDev: 0}}
	if _, err := h.rec.Record(bolt2); err == nil {
		t.Fatal("expected bolt stage order rejection")
	}
	// Stage 1 with out-of-tolerance preload deviation.
	bolt1 := EvidenceRequest{OperationID: "b1", RingID: h.id, Generation: 1, LogicalTime: 100, Operator: "op1",
		Bolt: &domain.BoltStageEvidence{Stage: 1, Generation: 1, LogicalTime: 100, PreloadDev: domain.Fixed(200000)}}
	if _, err := h.rec.Record(bolt1); err == nil {
		t.Fatal("expected preload tolerance rejection")
	}
}

func TestDeviceFaultDoesNotAdvance(t *testing.T) {
	h := newHarness(t)
	// Fault attempt produces a pending retry and no evidence.
	if _, err := h.rec.RecordDeviceAttempt(DeviceAttemptRequest{
		OperationID: "d1", RingID: h.id, Generation: 1, DeviceType: "gauge", CallNo: 1, LogicalTime: 1, FaultCode: "refused",
	}); err != nil {
		t.Fatalf("fault attempt: %v", err)
	}
	// Exceed retry limit (3) on the 4th fault -> anomaly error.
	for i := 0; i < 3; i++ {
		_, err := h.rec.RecordDeviceAttempt(DeviceAttemptRequest{
			OperationID: "d" + string(rune('a'+i)), RingID: h.id, Generation: 1, DeviceType: "gauge", CallNo: 1, LogicalTime: int64(i + 2), FaultCode: "refused",
		})
		if i == 2 {
			if err == nil {
				t.Fatal("expected retry exceeded")
			}
		}
	}
	// A successful reading can now be recorded.
	if _, err := h.rec.RecordDeviceAttempt(DeviceAttemptRequest{
		OperationID: "d-ok", RingID: h.id, Generation: 1, DeviceType: "gauge", CallNo: 2, LogicalTime: 10, FaultCode: "", Reading: fptr(domain.Fixed(500000)),
	}); err != nil {
		t.Fatalf("successful attempt: %v", err)
	}
}

func fptr(f domain.Fixed) *domain.Fixed { return &f }

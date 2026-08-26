package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/process"
	"shieldtunnel/ring"
	"shieldtunnel/store"
	"shieldtunnel/verdict"
)

func fullService(t *testing.T) *Service {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cat := catalog.NewStatic()
	return New(cat, ring.NewAggregate(cat, db), material.NewManager(db), process.NewRecorder(db), verdict.NewArbiter(db), db)
}

func doJSON(t *testing.T, srv *Service, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return rec.Code, out
}

func summaryFor(t *testing.T) domain.RuleSummary {
	t.Helper()
	sum, err := catalog.NewStatic().Summarize(domain.Section("澄江路—望塔站"), domain.RingType("通用楔形环"))
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	return sum
}

func lockBody(t *testing.T) ring.LockRequest {
	t.Helper()
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
	return ring.LockRequest{
		OperationID: "lock-1", Section: "澄江路—望塔站", RingNo: 1, RingType: "通用楔形环",
		Generation: 1, RuleSummary: summaryFor(t), LogicalTime: 0, Segments: segs, Joints: joints,
	}
}

func TestContractErrorEnvelopeSorted(t *testing.T) {
	srv := fullService(t)
	req := lockBody(t)
	req.RuleSummary = "stale"
	status, body := doJSON(t, srv, http.MethodPost, "/api/rings", req)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d want 422", status)
	}
	if body["code"] != "stale_summary" {
		t.Fatalf("code %v", body["code"])
	}
	if _, ok := body["reasons"]; !ok {
		t.Fatal("envelope missing reasons")
	}
	if body["operation_id"] != "lock-1" {
		t.Fatalf("operation_id %v", body["operation_id"])
	}
}

func TestContractFullAdmitFlow(t *testing.T) {
	srv := fullService(t)

	// Lock.
	status, body := doJSON(t, srv, http.MethodPost, "/api/rings", lockBody(t))
	if status != http.StatusOK {
		t.Fatalf("lock status %d body %v", status, body)
	}
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("missing task: %v", body)
	}
	id := task["id"].(string)

	// Acquire the leases needed by process steps.
	for _, res := range []domain.ResourceKind{domain.ResourceCleanBay, domain.ResourceGlueTable, domain.ResourceRoller, domain.ResourceErector, domain.ResourceTorqueTool} {
		status, _ := doJSON(t, srv, http.MethodPost, "/api/leases", material.LeaseRequest{
			OperationID: "lease-" + string(res), Resource: res, ResourceID: id, Holder: "op1", Start: 0, End: 10000,
		})
		if status != http.StatusOK {
			t.Fatalf("lease %s status %d", res, status)
		}
	}

	// Allocate materials.
	status, _ = doJSON(t, srv, http.MethodPost, "/api/rings/"+id+"/materials/allocate", material.AllocateRequest{
		OperationID: "alloc-1", Generation: 1, LogicalTime: 1, Slot: domain.SegmentSlot{SegmentSeq: 0},
		GasketBar: domain.GasketBar{ID: "bar-1", Batch: "GASKET-2026A", TotalLengthMM: 1000},
		Allocations: []domain.GasketAllocation{
			{BarID: "bar-1", Kind: "valid", LengthMM: 800},
			{BarID: "bar-1", Kind: "lap", LengthMM: 100},
			{BarID: "bar-1", Kind: "sample", LengthMM: 50},
			{BarID: "bar-1", Kind: "remainder", LengthMM: 30},
			{BarID: "bar-1", Kind: "loss", LengthMM: 20},
		},
		AdhesiveIssue: domain.AdhesiveIssue{Batch: "ADH-2026B", Generation: 1, TotalMg: 1000, AppliedMg: 700, RetainedMg: 100, RecoveredMg: 100, LossMg: 100},
	})
	if status != http.StatusOK {
		t.Fatalf("allocate status %d", status)
	}

	// Submit the process steps in order.
	steps := []string{"clean", "dry", "cut", "joint", "glue", "paste", "roll", "cure", "seat"}
	for i, kind := range steps {
		status, _ := doJSON(t, srv, http.MethodPost, "/api/rings/"+id+"/evidence", process.EvidenceRequest{
			OperationID: "ev-" + kind, Generation: 1, LogicalTime: int64((i + 1) * 10), Operator: "op1",
			Process: &domain.ProcessEvidence{Kind: kind, Generation: 1, LogicalTime: int64((i + 1) * 10), InstrumentID: "inst-1"},
		})
		if status != http.StatusOK {
			t.Fatalf("evidence %s status %d", kind, status)
		}
	}

	// Pressure traces.
	status, _ = doJSON(t, srv, http.MethodPost, "/api/rings/"+id+"/pressure-traces", verdict.PressureTraceRequest{
		OperationID: "trace-1", Generation: 1, Trace: domain.PressureTrace{Bay: 0, LogicalTime: 100, Pressure: 1000000},
	})
	if status != http.StatusOK {
		t.Fatalf("trace status %d", status)
	}

	// Two qualified reviews.
	for _, reviewer := range []string{"r1", "r2"} {
		status, _ = doJSON(t, srv, http.MethodPost, "/api/rings/"+id+"/reviews", verdict.ReviewRequest{
			OperationID: "rev-" + reviewer, Generation: 1, Reviewer: reviewer, Qualified: true, Approved: true,
		})
		if status != http.StatusOK {
			t.Fatalf("review %s status %d", reviewer, status)
		}
	}

	// Terminal admit decision produces a credential.
	status, body = doJSON(t, srv, http.MethodPost, "/api/rings/"+id+"/terminal-decisions", verdict.DecisionRequest{
		OperationID: "decide-1", Generation: 1, Kind: "admit",
	})
	if status != http.StatusOK {
		t.Fatalf("decide status %d body %v", status, body)
	}
	decision, ok := body["decision"].(map[string]any)
	if !ok {
		t.Fatalf("missing decision: %v", body)
	}
	if decision["kind"] != "admit" || decision["credential"] == "" {
		t.Fatalf("unexpected decision: %v", decision)
	}
}

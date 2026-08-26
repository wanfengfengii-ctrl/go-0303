package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSortReasonsDeterministicOrder(t *testing.T) {
	in := []Reason{
		{Code: CodeMissingEdge, Section: "B", RingNo: 1},
		{Code: CodeDuplicateSegment, Section: "A", RingNo: 2},
		{Code: CodeAngleSumMismatch, Section: "A", RingNo: 1},
		{Code: CodeBadWedge, Section: "A", RingNo: 1, SegmentSeq: 3},
	}
	got := SortReasons(in)
	want := []ErrorCode{CodeAngleSumMismatch, CodeBadWedge, CodeDuplicateSegment, CodeMissingEdge}
	for i, c := range want {
		if got[i].Code != c {
			t.Fatalf("pos %d got %s want %s", i, got[i].Code, c)
		}
	}
}

func TestSortReasonsDoesNotMutateInput(t *testing.T) {
	in := []Reason{{Code: CodeInternal}, {Code: CodeOK}}
	SortReasons(in)
	if in[0].Code != CodeInternal || in[1].Code != CodeOK {
		t.Fatal("SortReasons mutated its input")
	}
}

func TestErrorStringContainsCode(t *testing.T) {
	e := &Error{Code: CodeSectionMismatch, Operation: "op1", Reasons: []Reason{{Code: CodeSectionMismatch}}}
	if !strings.Contains(e.Error(), "section_mismatch") {
		t.Fatalf("error string missing code: %s", e.Error())
	}
	if !strings.Contains(e.Error(), "op1") {
		t.Fatalf("error string missing operation: %s", e.Error())
	}
}

func TestNewEnvelopeSortsReasons(t *testing.T) {
	env := NewEnvelope("op", CodeInternal,
		Reason{Code: CodeMissingEdge, Section: "Z"},
		Reason{Code: CodeBadWedge, Section: "A"})
	if env.Reasons[0].Code != CodeBadWedge {
		t.Fatalf("first reason %s want %s", env.Reasons[0].Code, CodeBadWedge)
	}
}

func TestEnvelopeJSONShape(t *testing.T) {
	env := NewEnvelope("op-1", CodeOK)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["code"] != "ok" || m["operation_id"] != "op-1" {
		t.Fatalf("unexpected envelope: %v", m)
	}
	if _, ok := m["reasons"]; !ok {
		t.Fatal("envelope missing reasons")
	}
}

func TestEmptyEnvelopeReasonsSerializeAsArray(t *testing.T) {
	env := NewEnvelope("op", CodeOK)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"code":"ok","operation_id":"op","reasons":[]}` {
		t.Fatalf("empty reasons not []: %s", b)
	}
}

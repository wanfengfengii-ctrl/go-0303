// Package httpapi implements the Go HTTP API and serves the embedded frontend
// (Go HTTP API 与前端). It exposes the documented JSON endpoints, the stable
// {code, operation_id, reasons[]} error envelope, and hosts the single-page
// dashboard at "/".
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/material"
	"shieldtunnel/process"
	"shieldtunnel/ring"
	"shieldtunnel/store"
	"shieldtunnel/verdict"
	"shieldtunnel/web"
)

// Service wires the five business components plus persistence behind the HTTP
// boundary. A nil component means the endpoint is not yet implemented and
// returns a stable CodeInternal envelope rather than a partial result.
type Service struct {
	Catalog  catalog.Catalog
	Rings    ring.RingAggregate
	Material material.MaterialManager
	Process  process.EvidenceRecorder
	Verdict  verdict.Arbitrator
	Store    store.Store
}

// New constructs an HTTP Service over the given components.
func New(c catalog.Catalog, r ring.RingAggregate, m material.MaterialManager,
	p process.EvidenceRecorder, v verdict.Arbitrator, s store.Store) *Service {
	return &Service{Catalog: c, Rings: r, Material: m, Process: p, Verdict: v, Store: s}
}

// Handler returns the routed HTTP handler including the frontend.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	mux.HandleFunc("POST /api/rings", s.handleLockRing)
	mux.HandleFunc("GET /api/rings/{section}/{ring}/graph", s.handleGraph)
	mux.HandleFunc("POST /api/rings/{id}/materials/allocate", s.handleAllocate)
	mux.HandleFunc("POST /api/leases", s.handleLease)
	mux.HandleFunc("POST /api/rings/{id}/evidence", s.handleEvidence)
	mux.HandleFunc("POST /api/device-attempts", s.handleDeviceAttempt)
	mux.HandleFunc("POST /api/rings/{id}/pressure-traces", s.handlePressureTrace)
	mux.HandleFunc("POST /api/rings/{id}/retests", s.handleRetest)
	mux.HandleFunc("POST /api/rings/{id}/reviews", s.handleReview)
	mux.HandleFunc("POST /api/rings/{id}/terminal-decisions", s.handleDecision)

	dist, err := web.Sub()
	if err != nil {
		// The build is embedded at compile time; this cannot happen in a
		// correctly built binary. Fall back to a bare mux.
		return mux
	}
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return mux
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, domain.NewEnvelope("", domain.CodeOK))
}

func (s *Service) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
		writeNotImplemented(w, "catalog")
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope("", domain.CodeOK), jsonField("summaries", s.Catalog.ListSummaries()))
}

// --- business handlers -----------------------------------------------------

func (s *Service) handleLockRing(w http.ResponseWriter, r *http.Request) {
	if s.Rings == nil {
		writeNotImplemented(w, "lock ring")
		return
	}
	var req ring.LockRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, domain.NewEnvelope("", domain.CodeInternal,
			domain.Reason{Code: domain.CodeInternal, Message: err.Error()}))
		return
	}
	task, err := s.Rings.Lock(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK),
		jsonField("task", task))
}

func (s *Service) handleGraph(w http.ResponseWriter, r *http.Request) {
	section := domain.Section(r.PathValue("section"))
	var ringNo domain.RingNo
	if _, err := fmt.Sscan(r.PathValue("ring"), &ringNo); err != nil {
		writeJSON(w, http.StatusBadRequest, domain.NewEnvelope("", domain.CodeInternal,
			domain.Reason{Code: domain.CodeInternal, Message: "invalid ring number"}))
		return
	}
	if s.Rings == nil {
		writeNotImplemented(w, "ring graph")
		return
	}
	task, err := s.Rings.Graph(section, ringNo)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope("", domain.CodeOK), jsonField("task", task))
}

func (s *Service) handleAllocate(w http.ResponseWriter, r *http.Request) {
	if s.Material == nil {
		writeNotImplemented(w, "material allocate")
		return
	}
	var req material.AllocateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	res, err := s.Material.Allocate(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("receipt", res.Receipt))
}

func (s *Service) handleLease(w http.ResponseWriter, r *http.Request) {
	if s.Material == nil {
		writeNotImplemented(w, "lease")
		return
	}
	var req material.LeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	lease, err := s.Material.AcquireLease(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("lease", lease))
}

func (s *Service) handleEvidence(w http.ResponseWriter, r *http.Request) {
	if s.Process == nil {
		writeNotImplemented(w, "evidence")
		return
	}
	var req process.EvidenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	receipt, err := s.Process.Record(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("receipt", receipt))
}

func (s *Service) handleDeviceAttempt(w http.ResponseWriter, r *http.Request) {
	if s.Process == nil {
		writeNotImplemented(w, "device attempt")
		return
	}
	var req process.DeviceAttemptRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	attempt, err := s.Process.RecordDeviceAttempt(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("attempt", attempt))
}

func (s *Service) handlePressureTrace(w http.ResponseWriter, r *http.Request) {
	if s.Verdict == nil {
		writeNotImplemented(w, "pressure trace")
		return
	}
	var req verdict.PressureTraceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	receipt, err := s.Verdict.AddPressureTrace(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("receipt", receipt))
}

func (s *Service) handleRetest(w http.ResponseWriter, r *http.Request) {
	if s.Verdict == nil {
		writeNotImplemented(w, "retest")
		return
	}
	var req verdict.RetestRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	retest, err := s.Verdict.PropagateRetest(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("retest", retest))
}

func (s *Service) handleReview(w http.ResponseWriter, r *http.Request) {
	if s.Verdict == nil {
		writeNotImplemented(w, "review")
		return
	}
	var req verdict.ReviewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	review, err := s.Verdict.SubmitReview(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("review", review))
}

func (s *Service) handleDecision(w http.ResponseWriter, r *http.Request) {
	if s.Verdict == nil {
		writeNotImplemented(w, "terminal decision")
		return
	}
	var req verdict.DecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadJSON(w, err)
		return
	}
	req.RingID = r.PathValue("id")
	decision, err := s.Verdict.Decide(req)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.NewEnvelope(req.OperationID, domain.CodeOK), jsonField("decision", decision))
}

// --- helpers ---------------------------------------------------------------

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dst)
}

func writeNotImplemented(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotImplemented, domain.NewEnvelope("", domain.CodeInternal,
		domain.Reason{Code: domain.CodeInternal, Message: what + " not implemented"}))
}

func writeBadJSON(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, domain.NewEnvelope("", domain.CodeInternal,
		domain.Reason{Code: domain.CodeInternal, Message: err.Error()}))
}

func writeDomainErr(w http.ResponseWriter, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		writeJSON(w, http.StatusUnprocessableEntity, domain.NewEnvelope(de.Operation, de.Code, de.Reasons...))
		return
	}
	writeJSON(w, http.StatusInternalServerError, domain.NewEnvelope("", domain.CodeInternal,
		domain.Reason{Code: domain.CodeInternal, Message: err.Error()}))
}

// jsonField attaches a named payload beside the envelope.
func jsonField(name string, v any) map[string]any {
	return map[string]any{name: v}
}

func writeJSON(w http.ResponseWriter, status int, env domain.Envelope, extra ...map[string]any) {
	out := map[string]any{"code": env.Code, "operation_id": env.OperationID, "reasons": env.Reasons}
	for _, e := range extra {
		for k, v := range e {
			out[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

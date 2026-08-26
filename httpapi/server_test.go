package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer() *Service {
	return New(nil, nil, nil, nil, nil, nil)
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "ok" {
		t.Fatalf("code %v want ok", body["code"])
	}
}

func TestNotImplementedReturnsStableEnvelope(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/rings", strings.NewReader(`{}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d want 501", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["code"] != "internal" {
		t.Fatalf("code %v want internal", body["code"])
	}
	if _, ok := body["reasons"]; !ok {
		t.Fatal("envelope missing reasons")
	}
}

func TestGraphInvalidRingNumber(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/rings/澄江路/notanumber/graph", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d want 400", rec.Code)
	}
}

func TestFrontendServed(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), "整环") {
		t.Fatalf("index.html missing expected content")
	}
}

func TestFrontendAssetsServed(t *testing.T) {
	srv := newTestServer()
	for _, path := range []string{"/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status %d want 200", path, rec.Code)
		}
	}
}

func TestEnvelopeContentType(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	srv.Handler().ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q want application/json", ct)
	}
}

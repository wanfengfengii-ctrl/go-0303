package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"shieldtunnel/domain"
	"shieldtunnel/httpapi"
	"shieldtunnel/material"
	"shieldtunnel/store"
)

func TestModel_LeaseOperationIDIsPersistentAndContentBound(t *testing.T) {
	type entrypoint string
	const (
		acquire entrypoint = "POST /api/leases"
		renew   entrypoint = "RenewLease"
	)
	type testCase struct {
		name       string
		entry      entrypoint
		second     material.LeaseRequest
		wantCode   domain.ErrorCode
		wantReplay bool
	}

	acquireRequest := material.LeaseRequest{
		OperationID: "lease-op", Resource: domain.ResourceGlueTable,
		ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 10,
	}
	renewRequest := material.LeaseRequest{
		OperationID: "renew-op", Resource: domain.ResourceGlueTable,
		ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 20,
	}
	cases := []testCase{
		{name: "acquire identical retry replays", entry: acquire, second: acquireRequest, wantReplay: true},
		{name: "acquire expired-boundary reuse cannot transfer holder", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op2", Start: 10, End: 20}, wantCode: domain.CodeIdempotentConflict},
		{name: "acquire holder is content", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op2", Start: 0, End: 10}, wantCode: domain.CodeIdempotentConflict},
		{name: "acquire resource is content", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceRoller, ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 10}, wantCode: domain.CodeIdempotentConflict},
		{name: "acquire resource id is content", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceGlueTable, ResourceID: "other-ring", Holder: "op1", Start: 0, End: 10}, wantCode: domain.CodeIdempotentConflict},
		{name: "acquire start is content", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op1", Start: 1, End: 10}, wantCode: domain.CodeIdempotentConflict},
		{name: "acquire end is content", entry: acquire, second: material.LeaseRequest{OperationID: "lease-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 11}, wantCode: domain.CodeIdempotentConflict},
		{name: "renew identical retry replays", entry: renew, second: renewRequest, wantReplay: true},
		{name: "renew holder is content", entry: renew, second: material.LeaseRequest{OperationID: "renew-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op2", Start: 0, End: 20}, wantCode: domain.CodeIdempotentConflict},
		{name: "renew resource is content", entry: renew, second: material.LeaseRequest{OperationID: "renew-op", Resource: domain.ResourceRoller, ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 20}, wantCode: domain.CodeIdempotentConflict},
		{name: "renew resource id is content", entry: renew, second: material.LeaseRequest{OperationID: "renew-op", Resource: domain.ResourceGlueTable, ResourceID: "other-ring", Holder: "op1", Start: 0, End: 20}, wantCode: domain.CodeIdempotentConflict},
		{name: "renew start is content", entry: renew, second: material.LeaseRequest{OperationID: "renew-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op1", Start: 1, End: 20}, wantCode: domain.CodeIdempotentConflict},
		{name: "renew end is content", entry: renew, second: material.LeaseRequest{OperationID: "renew-op", Resource: domain.ResourceGlueTable, ResourceID: "ring-lease", Holder: "op1", Start: 0, End: 21}, wantCode: domain.CodeIdempotentConflict},
	}

	invoke := func(t *testing.T, db *store.DB, entry entrypoint, req material.LeaseRequest) (domain.ResourceLease, domain.ErrorCode) {
		t.Helper()
		mgr := material.NewManager(db)
		if entry == acquire {
			payload, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal lease request: %v", err)
			}
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/api/leases", bytes.NewReader(payload))
			httpapi.New(nil, nil, mgr, nil, nil, db).Handler().ServeHTTP(rec, httpReq)
			var body struct {
				Code  domain.ErrorCode     `json:"code"`
				Lease domain.ResourceLease `json:"lease"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode lease response: %v", err)
			}
			if rec.Code != http.StatusOK && rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("POST /api/leases status %d body %s", rec.Code, rec.Body.String())
			}
			return body.Lease, body.Code
		}
		lease, err := mgr.RenewLease(req)
		if err == nil {
			return lease, domain.CodeOK
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("RenewLease returned non-domain error: %v", err)
		}
		return domain.ResourceLease{}, domainErr.Code
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "leases.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			firstRequest := acquireRequest
			if tc.entry == renew {
				seed := acquireRequest
				seed.OperationID = "seed-acquire"
				if _, code := invoke(t, db, acquire, seed); code != domain.CodeOK {
					t.Fatalf("seed acquire code %q", code)
				}
				firstRequest = renewRequest
			}
			firstLease, code := invoke(t, db, tc.entry, firstRequest)
			if code != domain.CodeOK {
				t.Fatalf("first %s code %q", tc.entry, code)
			}
			beforeSeq, err := db.LastSeq(context.Background())
			if err != nil {
				t.Fatalf("last sequence: %v", err)
			}
			var beforeReceipt *domain.OperationReceipt
			if err := db.WithTx(context.Background(), func(tx *store.Tx) error {
				var findErr error
				beforeReceipt, findErr = tx.FindReceipt(context.Background(), firstRequest.OperationID)
				return findErr
			}); err != nil || beforeReceipt == nil {
				t.Fatalf("persisted receipt: receipt=%v err=%v", beforeReceipt, err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			db, err = store.Open(path)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			secondLease, gotCode := invoke(t, db, tc.entry, tc.second)
			if tc.wantReplay {
				if gotCode != domain.CodeOK || !reflect.DeepEqual(secondLease, firstLease) {
					t.Fatalf("identical retry got lease %+v code %q; want original lease %+v and ok", secondLease, gotCode, firstLease)
				}
			} else if gotCode != tc.wantCode {
				t.Fatalf("changed-content retry code %q want %q", gotCode, tc.wantCode)
			}

			mgr := material.NewManager(db)
			afterLease, err := mgr.LookupLease(context.Background(), firstRequest.Resource, firstRequest.ResourceID)
			if err != nil || afterLease == nil || !reflect.DeepEqual(*afterLease, firstLease) {
				t.Fatalf("lease changed after retry: got %+v err=%v want %+v", afterLease, err, firstLease)
			}
			if tc.second.Resource != firstRequest.Resource || tc.second.ResourceID != firstRequest.ResourceID {
				other, err := mgr.LookupLease(context.Background(), tc.second.Resource, tc.second.ResourceID)
				if err != nil || other != nil {
					t.Fatalf("changed resource was mutated: lease=%+v err=%v", other, err)
				}
			}
			afterSeq, err := db.LastSeq(context.Background())
			if err != nil || afterSeq != beforeSeq {
				t.Fatalf("retry appended event: sequence %d want %d (err=%v)", afterSeq, beforeSeq, err)
			}
			var afterReceipt *domain.OperationReceipt
			if err := db.WithTx(context.Background(), func(tx *store.Tx) error {
				var findErr error
				afterReceipt, findErr = tx.FindReceipt(context.Background(), firstRequest.OperationID)
				return findErr
			}); err != nil || !reflect.DeepEqual(afterReceipt, beforeReceipt) {
				t.Fatalf("receipt changed after retry: before=%+v after=%+v err=%v", beforeReceipt, afterReceipt, err)
			}
		})
	}

	preserved := []struct {
		name string
		run  func(*testing.T, *material.Manager)
	}{
		{name: "different operation may take over at expiry", run: func(t *testing.T, mgr *material.Manager) {
			if _, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-a", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op1", Start: 0, End: 10}); err != nil {
				t.Fatalf("initial acquire: %v", err)
			}
			lease, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-b", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op2", Start: 10, End: 20})
			if err != nil || lease.Holder != "op2" {
				t.Fatalf("expiry takeover: lease=%+v err=%v", lease, err)
			}
		}},
		{name: "different operation lets current holder renew", run: func(t *testing.T, mgr *material.Manager) {
			if _, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-a", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op1", Start: 0, End: 10}); err != nil {
				t.Fatalf("initial acquire: %v", err)
			}
			lease, err := mgr.RenewLease(material.LeaseRequest{OperationID: "op-b", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op1", Start: 0, End: 20})
			if err != nil || lease.End != 20 {
				t.Fatalf("holder renew: lease=%+v err=%v", lease, err)
			}
		}},
		{name: "old holder late request stays rejected", run: func(t *testing.T, mgr *material.Manager) {
			if _, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-a", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op1", Start: 0, End: 10}); err != nil {
				t.Fatalf("initial acquire: %v", err)
			}
			if _, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-b", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op2", Start: 10, End: 20}); err != nil {
				t.Fatalf("expiry takeover: %v", err)
			}
			_, err := mgr.AcquireLease(material.LeaseRequest{OperationID: "op-c", Resource: domain.ResourceGlueTable, ResourceID: "ring", Holder: "op1", Start: 15, End: 25})
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeLeaseHolderMismatch {
				t.Fatalf("late old holder error=%v want lease_holder_mismatch", err)
			}
		}},
	}
	for _, tc := range preserved {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.OpenMemory()
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			tc.run(t, material.NewManager(db))
		})
	}
}

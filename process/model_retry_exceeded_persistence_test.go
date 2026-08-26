package process

import (
	"context"
	"path/filepath"
	"testing"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/ring"
	"shieldtunnel/store"
)

func TestModel_RetryExceededAttemptSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-attempts.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()

	h := &harness{db: db, rings: ring.NewAggregate(catalog.NewStatic(), db)}
	h.id = h.lock(t)
	recorder := NewRecorder(db)

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "faults below the retry limit remain pending and do not advance evidence",
			run: func(t *testing.T) {
				for i := 0; i < 3; i++ {
					attempt, err := recorder.RecordDeviceAttempt(DeviceAttemptRequest{
						OperationID: "pressure-timeout-" + string(rune('1'+i)),
						RingID:      h.id,
						Generation:  1,
						DeviceType:  "pressure_sensor",
						CallNo:      17,
						LogicalTime: int64(i + 1),
						FaultCode:   "timeout",
					})
					if err != nil {
						t.Fatalf("fault %d: %v", i+1, err)
					}
					if attempt.RetrySeq != i || attempt.FaultCode != "timeout" || attempt.Reading != nil {
						t.Fatalf("fault %d attempt = %+v", i+1, attempt)
					}
				}

				prefix, err := recorder.Prefix(h.id, 1)
				if err != nil {
					t.Fatalf("read prefix: %v", err)
				}
				if prefix != 0 {
					t.Fatalf("faults advanced business evidence prefix to %d", prefix)
				}
			},
		},
		{
			name: "limit-triggering fault returns retry exceeded and is durable",
			run: func(t *testing.T) {
				attempt, err := recorder.RecordDeviceAttempt(DeviceAttemptRequest{
					OperationID: "pressure-timeout-4",
					RingID:      h.id,
					Generation:  1,
					DeviceType:  "pressure_sensor",
					CallNo:      17,
					LogicalTime: 4,
					FaultCode:   "timeout",
				})
				if err == nil {
					t.Fatal("limit-triggering fault returned no error")
				}
				de, ok := err.(*domain.Error)
				if !ok || de.Code != domain.CodeRetryExceeded {
					t.Fatalf("limit-triggering error = %T %v", err, err)
				}
				if attempt.RetrySeq != 3 || attempt.FaultCode != "timeout" || attempt.Reading != nil {
					t.Fatalf("limit-triggering attempt = %+v", attempt)
				}

				if err := db.Close(); err != nil {
					t.Fatalf("close before restart: %v", err)
				}
				db = nil
				db, err = store.Open(path)
				if err != nil {
					t.Fatalf("reopen store: %v", err)
				}
				recorder = NewRecorder(db)

				var attempts []domain.DeviceAttempt
				var receipt *domain.OperationReceipt
				err = db.WithTx(context.Background(), func(tx *store.Tx) error {
					var queryErr error
					attempts, queryErr = tx.ListDeviceAttempts(context.Background(), h.id, 1)
					if queryErr != nil {
						return queryErr
					}
					receipt, queryErr = tx.FindReceipt(context.Background(), "pressure-timeout-4")
					return queryErr
				})
				if err != nil {
					t.Fatalf("read recovered attempts: %v", err)
				}
				if len(attempts) != 4 {
					t.Fatalf("recovered %d attempts, want 4", len(attempts))
				}
				last := attempts[3]
				if last.RetrySeq != 3 || last.FaultCode != "timeout" || last.LogicalTime != 4 || last.Reading != nil {
					t.Fatalf("recovered limit-triggering attempt = %+v", last)
				}
				if receipt == nil {
					t.Fatal("limit-triggering operation receipt was not recovered")
				}

				events, err := db.Replay(context.Background(), 0)
				if err != nil {
					t.Fatalf("replay audit stream: %v", err)
				}
				found := false
				for _, event := range events {
					if event.Operation == "pressure-timeout-4" && event.Kind == store.KindDevice {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("limit-triggering device fact missing from recovered audit stream")
				}
			},
		},
		{
			name: "successful reading keeps existing idempotent semantics",
			run: func(t *testing.T) {
				reading := domain.Fixed(500000)
				req := DeviceAttemptRequest{
					OperationID: "pressure-success",
					RingID:      h.id,
					Generation:  1,
					DeviceType:  "pressure_sensor",
					CallNo:      18,
					LogicalTime: 5,
					Reading:     &reading,
				}
				first, err := recorder.RecordDeviceAttempt(req)
				if err != nil {
					t.Fatalf("first successful reading: %v", err)
				}
				second, err := recorder.RecordDeviceAttempt(req)
				if err != nil {
					t.Fatalf("idempotent successful reading: %v", err)
				}
				if first.Reading == nil || second.Reading == nil || *first.Reading != reading || *second.Reading != reading || first.RetrySeq != second.RetrySeq {
					t.Fatalf("idempotent results differ: first=%+v second=%+v", first, second)
				}

				var attempts []domain.DeviceAttempt
				err = db.WithTx(context.Background(), func(tx *store.Tx) error {
					var queryErr error
					attempts, queryErr = tx.ListDeviceAttempts(context.Background(), h.id, 1)
					return queryErr
				})
				if err != nil {
					t.Fatalf("list attempts: %v", err)
				}
				if len(attempts) != 5 {
					t.Fatalf("idempotent success produced %d total attempts, want 5", len(attempts))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// Recorder is the concrete paste/assembly/tightening evidence recorder. It
// enforces the dependency state machine, continuous prefix invariants, open
// time limits, lease validity and fixed-point tolerances, and records scripted
// device calls without ever fabricating a measurement.
type Recorder struct {
	db *store.DB
}

// NewRecorder constructs the evidence recorder over a store.
func NewRecorder(db *store.DB) *Recorder {
	return &Recorder{db: db}
}

// Record appends one process, bolt-stage or geometry evidence event, enforcing
// the locked dependency chain. It is idempotent by operation id.
func (r *Recorder) Record(req EvidenceRequest) (domain.OperationReceipt, error) {
	hash := contentHash(struct {
		Generation  domain.Generation
		LogicalTime int64
		Operator    string
		Process     *domain.ProcessEvidence
		Bolt        *domain.BoltStageEvidence
		Geometry    *domain.GeometryEvidence
	}{req.Generation, req.LogicalTime, req.Operator, req.Process, req.Bolt, req.Geometry})

	var receipt domain.OperationReceipt
	err := r.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()

		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				receipt = *rc
				return nil
			}
			return &domain.Error{Code: domain.CodeIdempotentConflict, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeIdempotentConflict, Message: "operation id reused with different content"}}}
		}

		task, err := tx.FindRingTaskByID(ctx, req.RingID)
		if err != nil {
			return err
		}
		if task == nil {
			return &domain.Error{Code: domain.CodeNotFound, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeNotFound, Message: "ring not locked"}}}
		}
		if task.Generation != req.Generation {
			return &domain.Error{Code: domain.CodeStaleGeneration, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeStaleGeneration, Generation: req.Generation, Message: "stale generation"}}}
		}

		records, err := tx.ListEvidenceRecords(ctx, req.RingID, req.Generation)
		if err != nil {
			return err
		}
		state := deriveState(records)

		var ev *store.EvidenceRecord
		switch {
		case req.Process != nil:
			ev, err = r.validateProcess(ctx, tx, req, task.Rule.Thresholds, state)
		case req.Bolt != nil:
			ev, err = r.validateBolt(ctx, tx, req, task.Rule.Thresholds, state)
		case req.Geometry != nil:
			ev, err = r.validateGeometry(ctx, tx, req, task.Rule.Thresholds, state)
		default:
			return &domain.Error{Code: domain.CodeInternal, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: "no evidence payload"}}}
		}
		if err != nil {
			return err
		}

		if err := tx.SaveEvidence(ctx, *ev); err != nil {
			return err
		}
		receipt = domain.OperationReceipt{OperationID: req.OperationID, ContentHash: hash, Result: "ok"}
		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, "ok"); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindEvidence, Payload: ev})
	})
	return receipt, err
}

// validateProcess validates a process step against the dependency state machine.
func (r *Recorder) validateProcess(ctx context.Context, tx *store.Tx, req EvidenceRequest, th domain.Thresholds, state processState) (*store.EvidenceRecord, error) {
	kind := req.Process.Kind
	idx := stepIndex(kind)
	if idx < 0 {
		return nil, &domain.Error{Code: domain.CodeInternal, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: "unknown process step"}}}
	}
	if state.prefix >= len(stepOrder) {
		return nil, &domain.Error{Code: domain.CodePasteGap, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodePasteGap, Message: "process already complete"}}}
	}
	if kind != stepOrder[state.prefix] {
		return nil, &domain.Error{Code: domain.CodePasteGap, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodePasteGap, Message: "process step out of order"}}}
	}
	if req.LogicalTime < state.lastTime {
		return nil, &domain.Error{Code: domain.CodeLogicalTimeOrder, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "logical time cannot regress"}}}
	}
	// Open time: glue -> paste must be within OpenTimeMax (start inclusive, end exclusive).
	if kind == "paste" && state.glueTime >= 0 && req.LogicalTime-state.glueTime >= th.OpenTimeMax {
		return nil, &domain.Error{Code: domain.CodeOpenTimeExpired, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeOpenTimeExpired, Message: "adhesive open time expired"}}}
	}
	// Required lease must be held by the operator and still valid.
	if res, ok := requiredLease(kind); ok {
		if err := checkLease(ctx, tx, req, res); err != nil {
			return nil, err
		}
	}
	return &store.EvidenceRecord{
		RingID: req.RingID, Generation: req.Generation, Kind: kind,
		LogicalTime: req.LogicalTime, PrefixLen: state.prefix + 1,
		Instrument: req.Process.InstrumentID,
	}, nil
}

// validateBolt validates a staged bolt preload evidence.
func (r *Recorder) validateBolt(ctx context.Context, tx *store.Tx, req EvidenceRequest, th domain.Thresholds, state processState) (*store.EvidenceRecord, error) {
	if !state.atSeat() {
		return nil, &domain.Error{Code: domain.CodePasteGap, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodePasteGap, Message: "seating must complete before tightening"}}}
	}
	if req.Bolt.Stage != state.boltStage+1 {
		return nil, &domain.Error{Code: domain.CodePasteGap, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodePasteGap, Message: "bolt stage out of order"}}}
	}
	if req.LogicalTime < state.lastTime {
		return nil, &domain.Error{Code: domain.CodeLogicalTimeOrder, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "logical time cannot regress"}}}
	}
	if abs64(int64(req.Bolt.PreloadDev)) > int64(th.PreloadDevMax) {
		return nil, &domain.Error{Code: domain.CodeOutOfTolerance, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeOutOfTolerance, Message: "preload deviation out of tolerance"}}}
	}
	if err := checkLease(ctx, tx, req, domain.ResourceTorqueTool); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(boltPayload{Stage: req.Bolt.Stage, PreloadDev: int64(req.Bolt.PreloadDev)})
	return &store.EvidenceRecord{
		RingID: req.RingID, Generation: req.Generation, Kind: "bolt",
		LogicalTime: req.LogicalTime, PrefixLen: state.prefix, Instrument: "",
		Payload: json.RawMessage(payload),
	}, nil
}

// validateGeometry validates a seam geometry measurement against tolerances.
func (r *Recorder) validateGeometry(ctx context.Context, tx *store.Tx, req EvidenceRequest, th domain.Thresholds, state processState) (*store.EvidenceRecord, error) {
	if !state.atSeat() {
		return nil, &domain.Error{Code: domain.CodePasteGap, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodePasteGap, Message: "seating must complete before geometry"}}}
	}
	if req.LogicalTime < state.lastTime {
		return nil, &domain.Error{Code: domain.CodeLogicalTimeOrder, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeLogicalTimeOrder, Message: "logical time cannot regress"}}}
	}
	v := int64(req.Geometry.Value)
	switch req.Geometry.Kind {
	case "opening":
		if v < 0 || v > int64(th.OpeningMax) {
			return nil, &domain.Error{Code: domain.CodeOutOfTolerance, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeOutOfTolerance, Message: "joint opening out of tolerance"}}}
		}
	case "offset":
		if v < 0 || v > int64(th.OffsetMax) {
			return nil, &domain.Error{Code: domain.CodeOutOfTolerance, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeOutOfTolerance, Message: "segment offset out of tolerance"}}}
		}
	case "compression":
		if v < int64(th.CompressionMin) {
			return nil, &domain.Error{Code: domain.CodeOutOfTolerance, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeOutOfTolerance, Message: "gasket compression below minimum"}}}
		}
	default:
		return nil, &domain.Error{Code: domain.CodeInternal, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: "unknown geometry kind"}}}
	}
	payload, _ := json.Marshal(geometryPayload{Kind: req.Geometry.Kind, Value: v})
	return &store.EvidenceRecord{
		RingID: req.RingID, Generation: req.Generation, Kind: "geometry",
		LogicalTime: req.LogicalTime, PrefixLen: state.prefix, Instrument: req.Geometry.InstrumentID,
		Payload: json.RawMessage(payload),
	}, nil
}

// checkLease verifies the operator holds a valid lease for a resource on the
// ring. The resource id is the ring id.
func checkLease(ctx context.Context, tx *store.Tx, req EvidenceRequest, res domain.ResourceKind) error {
	lease, err := tx.FindLease(ctx, res, req.RingID)
	if err != nil {
		return err
	}
	if lease == nil || lease.Holder != req.Operator || req.LogicalTime < lease.Start || req.LogicalTime >= lease.End {
		return &domain.Error{Code: domain.CodeLeaseExpired, Operation: req.OperationID,
			Reasons: []domain.Reason{{Code: domain.CodeLeaseExpired, Message: "required lease missing, expired or held by another operator"}}}
	}
	return nil
}

// RecordDeviceAttempt appends a scripted device call. A fault only produces a
// pending retry record and never advances state; exceeding the retry limit
// becomes an anomaly. Idempotent by operation id.
func (r *Recorder) RecordDeviceAttempt(req DeviceAttemptRequest) (domain.DeviceAttempt, error) {
	hash := contentHash(struct {
		Generation  domain.Generation
		DeviceType  string
		CallNo      int
		LogicalTime int64
		FaultCode   string
		Reading     *domain.Fixed
	}{req.Generation, req.DeviceType, req.CallNo, req.LogicalTime, req.FaultCode, req.Reading})

	var out domain.DeviceAttempt
	err := r.db.WithTx(context.Background(), func(tx *store.Tx) error {
		ctx := context.Background()

		if rc, err := tx.FindReceipt(ctx, req.OperationID); err != nil {
			return err
		} else if rc != nil {
			if rc.ContentHash == hash {
				return decodeAttempt(rc.Result, &out)
			}
			return &domain.Error{Code: domain.CodeIdempotentConflict, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeIdempotentConflict, Message: "operation id reused with different content"}}}
		}

		task, err := tx.FindRingTaskByID(ctx, req.RingID)
		if err != nil {
			return err
		}
		if task == nil {
			return &domain.Error{Code: domain.CodeNotFound, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeNotFound, Message: "ring not locked"}}}
		}
		if task.Generation != req.Generation {
			return &domain.Error{Code: domain.CodeStaleGeneration, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeStaleGeneration, Generation: req.Generation, Message: "stale generation"}}}
		}

		prior, err := tx.ListDeviceAttempts(ctx, req.RingID, req.Generation)
		if err != nil {
			return err
		}
		retrySeq := 0
		for _, a := range prior {
			if a.DeviceType == req.DeviceType && a.CallNo == req.CallNo {
				retrySeq++
			}
		}

		out = domain.DeviceAttempt{
			DeviceType: req.DeviceType, CallNo: req.CallNo, LogicalTime: req.LogicalTime,
			RetrySeq: retrySeq, FaultCode: req.FaultCode, Reading: req.Reading,
		}
		if err := tx.SaveDeviceAttempt(ctx, req.RingID, req.Generation, out); err != nil {
			return err
		}

		if req.FaultCode != "" && retrySeq >= task.Rule.Thresholds.RetryLimit {
			return &domain.Error{Code: domain.CodeRetryExceeded, Operation: req.OperationID,
				Reasons: []domain.Reason{{Code: domain.CodeRetryExceeded, Message: "device retry limit exceeded"}}}
		}

		if _, err := tx.SaveReceipt(ctx, req.OperationID, hash, encodeAttempt(out)); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindDevice, Payload: out})
	})
	if err != nil {
		return domain.DeviceAttempt{}, err
	}
	return out, nil
}

// Prefix returns the current continuous process prefix length for a ring
// generation, rebuilt from stored evidence.
func (r *Recorder) Prefix(ringID string, generation domain.Generation) (int, error) {
	var prefix int
	err := r.db.WithTx(context.Background(), func(tx *store.Tx) error {
		records, err := tx.ListEvidenceRecords(context.Background(), ringID, generation)
		if err != nil {
			return err
		}
		prefix = deriveState(records).prefix
		return nil
	})
	return prefix, err
}

func contentHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func encodeAttempt(a domain.DeviceAttempt) string {
	b, _ := json.Marshal(a)
	return string(b)
}

func decodeAttempt(result string, out *domain.DeviceAttempt) error {
	return json.Unmarshal([]byte(result), out)
}

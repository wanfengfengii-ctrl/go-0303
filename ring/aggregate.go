package ring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/store"
)

// Aggregate is the concrete ring-task and joint-closure aggregate. It validates
// lock requests against the rule catalogue and persists immutable task
// snapshots plus their append-only event.
type Aggregate struct {
	catalog catalog.Catalog
	db      *store.DB
}

// NewAggregate constructs the ring aggregate over a catalogue and store.
func NewAggregate(c catalog.Catalog, db *store.DB) *Aggregate {
	return &Aggregate{catalog: c, db: db}
}

// Lock validates and persists a lock request. All reasons are collected and
// returned deterministically sorted; a valid request produces an immutable
// task snapshot and a lock event in a single transaction.
func (a *Aggregate) Lock(req LockRequest) (domain.RingTask, error) {
	snap, err := a.catalog.Snapshot(req.Section, req.RingType)
	if err != nil {
		return domain.RingTask{}, err
	}

	reasons := closureReasons(snap, req)
	for _, s := range req.Segments {
		if tmpl, ok := templateFor(snap, s.Type); ok {
			if err := a.catalog.ValidateGeometry(tmpl, s.Groove, s.Holes); err != nil {
				if de, ok := err.(*domain.Error); ok {
					for _, r := range de.Reasons {
						r.Section = req.Section
						r.RingNo = req.RingNo
						r.SegmentSeq = s.Seq
						reasons = append(reasons, r)
					}
				}
			}
		}
	}

	if len(reasons) > 0 {
		sorted := domain.SortReasons(reasons)
		return domain.RingTask{}, &domain.Error{Code: sorted[0].Code, Operation: req.OperationID, Reasons: sorted}
	}

	task := domain.RingTask{
		ID:           taskID(req.Section, req.RingNo),
		Section:      req.Section,
		RingNo:       req.RingNo,
		Generation:   req.Generation,
		Rule:         snap,
		Segments:     sortSegments(req.Segments),
		Joints:       req.Joints,
		SealSections: req.SealSections,
		LockedAt:     req.LogicalTime,
	}

	err = a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRingTask(context.Background(), task); err != nil {
			return err
		}
		return tx.AppendEvent(store.Event{Operation: req.OperationID, Kind: store.KindLock, Payload: task})
	})
	if err != nil {
		return domain.RingTask{}, &domain.Error{Code: domain.CodeInternal, Operation: req.OperationID, Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: err.Error()}}}
	}
	return task, nil
}

// Graph returns the active closed-loop graph for a section/ring.
func (a *Aggregate) Graph(section domain.Section, ringNo domain.RingNo) (domain.RingTask, error) {
	var task *domain.RingTask
	err := a.db.WithTx(context.Background(), func(tx *store.Tx) error {
		t, e := tx.FindRingTask(context.Background(), section, ringNo)
		task = t
		return e
	})
	if err != nil {
		return domain.RingTask{}, &domain.Error{Code: domain.CodeInternal, Reasons: []domain.Reason{{Code: domain.CodeInternal, Message: err.Error()}}}
	}
	if task == nil {
		return domain.RingTask{}, &domain.Error{Code: domain.CodeNotFound, Reasons: []domain.Reason{{Code: domain.CodeNotFound, Section: section, RingNo: ringNo, Message: "ring not locked"}}}
	}
	return *task, nil
}

// FindByID resolves a task by its opaque id (used by downstream components).
func (a *Aggregate) FindByID(ctx context.Context, id string) (*domain.RingTask, error) {
	var task *domain.RingTask
	err := a.db.WithTx(ctx, func(tx *store.Tx) error {
		t, e := tx.FindRingTaskByID(ctx, id)
		task = t
		return e
	})
	return task, err
}

// templateFor returns the full segment template for a segment type.
func templateFor(snap domain.RuleSnapshot, t domain.SegmentType) (domain.SegmentTemplate, bool) {
	for _, tmpl := range snap.SegmentTemplate {
		if tmpl.Type == t {
			return tmpl, true
		}
	}
	return domain.SegmentTemplate{}, false
}

// sortSegments returns segments ordered by sequence number for a stable graph.
func sortSegments(segs []domain.Segment) []domain.Segment {
	out := make([]domain.Segment, len(segs))
	copy(out, segs)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Seq < out[j-1].Seq; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// taskID derives a stable, URL-safe opaque id from the aggregate key. The id is
// independent of generation so retest propagation can bump the generation in
// place while every derived table keeps referencing the same ring row.
func taskID(section domain.Section, ringNo domain.RingNo) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", section, ringNo)))
	return hex.EncodeToString(h[:])[:16]
}

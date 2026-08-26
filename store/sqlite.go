package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"

	"shieldtunnel/domain"
)

// DB is the concrete SQLite persistence boundary. A single mutex serializes
// every transaction, and each SQLite connection is opened with foreign keys,
// unique indexes and immediate write transactions so that concurrent writers
// observe exactly one committed winner while the others read the committed
// result. The events table is the append-only fact log; derived tables are the
// query-facing projections rebuilt from those facts at startup.
type DB struct {
	mu  sync.Mutex
	sql *sql.DB
}

// Open opens (or creates) a SQLite database at path and applies the schema.
// It also runs a startup consistency verification so a crash-recovered store
// is validated before it is served.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	return open(dsn)
}

// OpenMemory opens an isolated in-memory SQLite database (used by tests).
func OpenMemory() (*DB, error) {
	return open("file::memory:?_pragma=foreign_keys(1)")
}

func open(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes all statements and avoids SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)
	d := &DB{sql: sqlDB}
	if err := d.applySchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := d.verify(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the underlying connection.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sql.Close()
}

// WithTx serializes fn inside a single SQLite write transaction. Any error
// returned by fn rolls back the transaction, so a failed operation leaves no
// visible partial state (no material deduction, binding, lease, evidence or
// credential).
func (d *DB) WithTx(ctx context.Context, fn func(tx *Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	sqlTx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	t := &Tx{sql: sqlTx}
	if err := fn(t); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	return sqlTx.Commit()
}

// Append implements Store by recording one event in its own transaction.
func (d *DB) Append(ctx context.Context, event Event) (int64, error) {
	var seq int64
	err := d.WithTx(ctx, func(tx *Tx) error {
		var e error
		seq, e = tx.appendEvent(event)
		return e
	})
	return seq, err
}

// LastSeq implements Store.
func (d *DB) LastSeq(ctx context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var seq int64
	err := d.sql.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events").Scan(&seq)
	return seq, err
}

// Replay implements Store, returning committed events from the given offset.
func (d *DB) Replay(ctx context.Context, from int64) ([]Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.QueryContext(ctx,
		"SELECT seq, operation, kind, payload, domain_err FROM events WHERE seq > ? ORDER BY seq", from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload, domainErr string
		if err := rows.Scan(&e.Seq, &e.Operation, &e.Kind, &payload, &domainErr); err != nil {
			return nil, err
		}
		if payload != "" {
			e.Payload = json.RawMessage(payload)
		}
		if domainErr != "" {
			var de domain.Error
			if json.Unmarshal([]byte(domainErr), &de) == nil {
				e.DomainErr = &de
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Tx is a single serialized transaction. All query methods operate on the
// embedded SQL transaction so multi-statement operations are atomic.
type Tx struct {
	sql *sql.Tx
}

func (t *Tx) appendEvent(e Event) (int64, error) {
	payload := ""
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return 0, err
		}
		payload = string(b)
	}
	domainErr := ""
	if e.DomainErr != nil {
		b, _ := json.Marshal(e.DomainErr)
		domainErr = string(b)
	}
	res, err := t.sql.Exec("INSERT INTO events(operation, kind, payload, domain_err) VALUES(?,?,?,?)",
		e.Operation, e.Kind, payload, domainErr)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AppendEvent appends one audit event inside the current transaction.
func (t *Tx) AppendEvent(e Event) error {
	_, err := t.appendEvent(e)
	return err
}

// AppendRejected records a rejected operation for audit without any state change.
func (t *Tx) AppendRejected(op string, de *domain.Error) error {
	return t.AppendEvent(Event{Operation: op, Kind: "rejected", DomainErr: de})
}

// --- idempotency ------------------------------------------------------------

// FindReceipt returns the stored idempotent receipt for an operation, or nil.
func (t *Tx) FindReceipt(ctx context.Context, opID string) (*domain.OperationReceipt, error) {
	var hash, result string
	err := t.sql.QueryRowContext(ctx,
		"SELECT content_hash, result FROM operations WHERE operation_id = ?", opID).Scan(&hash, &result)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.OperationReceipt{OperationID: opID, ContentHash: hash, Result: result}, nil
}

// SaveReceipt records a new idempotent receipt. It returns true if inserted and
// false if the operation id already existed (caller must then compare hashes).
func (t *Tx) SaveReceipt(ctx context.Context, opID, hash, result string) (bool, error) {
	res, err := t.sql.ExecContext(ctx,
		"INSERT OR IGNORE INTO operations(operation_id, content_hash, result) VALUES(?,?,?)",
		opID, hash, result)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// --- ring tasks -------------------------------------------------------------

// SaveRingTask persists an immutable locked ring task. The unique index on
// (section, ring_no) enforces that a ring has exactly one active lock.
func (t *Tx) SaveRingTask(ctx context.Context, task domain.RingTask) error {
	b, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = t.sql.ExecContext(ctx,
		"INSERT OR REPLACE INTO ring_tasks(id, section, ring_no, generation, payload) VALUES(?,?,?,?,?)",
		task.ID, string(task.Section), int(task.RingNo), int(task.Generation), string(b))
	return err
}

// FindRingTask returns the active (highest-generation) task for a section/ring.
func (t *Tx) FindRingTask(ctx context.Context, section domain.Section, ringNo domain.RingNo) (*domain.RingTask, error) {
	var payload string
	err := t.sql.QueryRowContext(ctx,
		"SELECT payload FROM ring_tasks WHERE section=? AND ring_no=? ORDER BY generation DESC LIMIT 1",
		string(section), int(ringNo)).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeRingTask(payload)
}

// FindRingTaskByID returns a task by its opaque id.
func (t *Tx) FindRingTaskByID(ctx context.Context, id string) (*domain.RingTask, error) {
	var payload string
	err := t.sql.QueryRowContext(ctx, "SELECT payload FROM ring_tasks WHERE id=?", id).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeRingTask(payload)
}

func decodeRingTask(payload string) (*domain.RingTask, error) {
	var task domain.RingTask
	if err := json.Unmarshal([]byte(payload), &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// --- gasket bars ------------------------------------------------------------

// CreateGasketBar inserts a gasket bar bound to a single slot. It returns
// false when the bar identity already exists (so a concurrent duplicate is
// rejected by the unique constraint and can read the committed winner).
func (t *Tx) CreateGasketBar(ctx context.Context, bar domain.GasketBar, slot domain.SegmentSlot) (bool, error) {
	res, err := t.sql.ExecContext(ctx,
		"INSERT OR IGNORE INTO gasket_bars(id, batch, total_mm, slot_seq) VALUES(?,?,?,?)",
		bar.ID, bar.Batch, bar.TotalLengthMM, slot.SegmentSeq)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FindGasketBar returns a gasket bar by identity, or nil.
func (t *Tx) FindGasketBar(ctx context.Context, id string) (*domain.GasketBar, error) {
	var batch string
	var totalMM int64
	var slotSeq sql.NullInt64
	err := t.sql.QueryRowContext(ctx,
		"SELECT batch, total_mm, slot_seq FROM gasket_bars WHERE id=?", id).Scan(&batch, &totalMM, &slotSeq)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bar := &domain.GasketBar{ID: id, Batch: batch, TotalLengthMM: totalMM}
	if slotSeq.Valid {
		bar.BoundToSlot = &domain.SegmentSlot{SegmentSeq: int(slotSeq.Int64)}
	}
	return bar, nil
}

// GasketBinding is a committed gasket bar bound to a single slot.
type GasketBinding struct {
	BarID   string
	Batch   string
	SlotSeq int
}

// ListGasketBindings returns the committed gasket-bar bindings, used by retest
// propagation to find segments sharing a gasket bar batch.
func (t *Tx) ListGasketBindings(ctx context.Context) ([]GasketBinding, error) {
	rows, err := t.sql.QueryContext(ctx, "SELECT id, batch, slot_seq FROM gasket_bars WHERE slot_seq IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GasketBinding
	for rows.Next() {
		var b GasketBinding
		if err := rows.Scan(&b.BarID, &b.Batch, &b.SlotSeq); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// --- material ledger ---------------------------------------------------------

// SaveLedgerEntries appends immutable ledger rows in one statement.
func (t *Tx) SaveLedgerEntries(ctx context.Context, entries []domain.MaterialLedgerEntry) error {
	for _, e := range entries {
		if _, err := t.sql.ExecContext(ctx,
			"INSERT INTO material_ledger(kind, delta_mm, delta_mg, operation) VALUES(?,?,?,?)",
			e.Kind, e.DeltaMM, e.DeltaMg, e.Operation); err != nil {
			return err
		}
	}
	return nil
}

// ListLedger returns all committed ledger rows in insertion order.
func (t *Tx) ListLedger(ctx context.Context) ([]domain.MaterialLedgerEntry, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT seq, kind, delta_mm, delta_mg, operation FROM material_ledger ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MaterialLedgerEntry
	for rows.Next() {
		var e domain.MaterialLedgerEntry
		if err := rows.Scan(&e.Seq, &e.Kind, &e.DeltaMM, &e.DeltaMg, &e.Operation); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- leases ------------------------------------------------------------------

// SaveLease inserts a lease, returning the committed lease and whether it was
// newly created. On a unique (resource, resource_id) conflict the existing
// lease is returned so the caller can apply expiry/holder rules.
func (t *Tx) SaveLease(ctx context.Context, lease domain.ResourceLease) (*domain.ResourceLease, bool, error) {
	res, err := t.sql.ExecContext(ctx,
		"INSERT OR IGNORE INTO leases(resource, resource_id, holder, start, end) VALUES(?,?,?,?,?)",
		string(lease.Resource), lease.ResourceID, lease.Holder, lease.Start, lease.End)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		return &lease, true, nil
	}
	existing, err := t.FindLease(ctx, lease.Resource, lease.ResourceID)
	return existing, false, err
}

// ReplaceLease overwrites the lease for a resource after the caller has
// applied expiry/holder rules under the serialized transaction.
func (t *Tx) ReplaceLease(ctx context.Context, lease domain.ResourceLease) error {
	_, err := t.sql.ExecContext(ctx,
		"INSERT OR REPLACE INTO leases(resource, resource_id, holder, start, end) VALUES(?,?,?,?,?)",
		string(lease.Resource), lease.ResourceID, lease.Holder, lease.Start, lease.End)
	return err
}

// FindLease returns the current lease for a resource, or nil.
func (t *Tx) FindLease(ctx context.Context, resource domain.ResourceKind, id string) (*domain.ResourceLease, error) {
	var holder string
	var start, end int64
	err := t.sql.QueryRowContext(ctx,
		"SELECT holder, start, end FROM leases WHERE resource=? AND resource_id=?",
		string(resource), id).Scan(&holder, &start, &end)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.ResourceLease{Resource: resource, ResourceID: id, Holder: holder, Start: start, End: end}, nil
}

// --- process evidence ---------------------------------------------------------

// SaveEvidence appends one process/bolt/geometry evidence record.
func (t *Tx) SaveEvidence(ctx context.Context, e EvidenceRecord) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	_, err = t.sql.ExecContext(ctx,
		"INSERT INTO process_evidence(ring_id, generation, kind, logical_time, prefix_len, instrument, payload) VALUES(?,?,?,?,?,?,?)",
		e.RingID, int(e.Generation), e.Kind, e.LogicalTime, e.PrefixLen, e.Instrument, string(payload))
	return err
}

// ListEvidence returns evidence for a ring generation in logical-time order.
func (t *Tx) ListEvidence(ctx context.Context, ringID string, gen domain.Generation) ([]domain.ProcessEvidence, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT kind, logical_time, prefix_len, instrument FROM process_evidence WHERE ring_id=? AND generation=? ORDER BY seq",
		ringID, int(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ProcessEvidence
	for rows.Next() {
		var e domain.ProcessEvidence
		e.Generation = gen
		if err := rows.Scan(&e.Kind, &e.LogicalTime, &e.PrefixLen, &e.InstrumentID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EvidenceRecord is the persisted form of one evidence event.
type EvidenceRecord struct {
	RingID      string
	Generation  domain.Generation
	Kind        string
	LogicalTime int64
	PrefixLen   int
	Instrument  string
	Payload     any
}

// ListEvidenceRecords returns full evidence records (including payload) for a
// ring generation in insertion order, used by the recorder to rebuild the
// dependency state machine at each submission.
func (t *Tx) ListEvidenceRecords(ctx context.Context, ringID string, gen domain.Generation) ([]EvidenceRecord, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT kind, logical_time, prefix_len, instrument, payload FROM process_evidence WHERE ring_id=? AND generation=? ORDER BY seq",
		ringID, int(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceRecord
	for rows.Next() {
		var e EvidenceRecord
		e.RingID = ringID
		e.Generation = gen
		var payload string
		if err := rows.Scan(&e.Kind, &e.LogicalTime, &e.PrefixLen, &e.Instrument, &payload); err != nil {
			return nil, err
		}
		if payload != "" {
			e.Payload = json.RawMessage(payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- device attempts ---------------------------------------------------------

// SaveDeviceAttempt appends a scripted device call record.
func (t *Tx) SaveDeviceAttempt(ctx context.Context, ringID string, gen domain.Generation, a domain.DeviceAttempt) error {
	var reading *int64
	if a.Reading != nil {
		v := int64(*a.Reading)
		reading = &v
	}
	_, err := t.sql.ExecContext(ctx,
		"INSERT INTO device_attempts(ring_id, generation, device_type, call_no, logical_time, retry_seq, fault_code, reading) VALUES(?,?,?,?,?,?,?,?)",
		ringID, int(gen), a.DeviceType, a.CallNo, a.LogicalTime, a.RetrySeq, a.FaultCode, reading)
	return err
}

// ListDeviceAttempts returns device calls for a ring generation.
func (t *Tx) ListDeviceAttempts(ctx context.Context, ringID string, gen domain.Generation) ([]domain.DeviceAttempt, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT device_type, call_no, logical_time, retry_seq, fault_code, reading FROM device_attempts WHERE ring_id=? AND generation=? ORDER BY seq",
		ringID, int(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DeviceAttempt
	for rows.Next() {
		var a domain.DeviceAttempt
		var reading sql.NullInt64
		if err := rows.Scan(&a.DeviceType, &a.CallNo, &a.LogicalTime, &a.RetrySeq, &a.FaultCode, &reading); err != nil {
			return nil, err
		}
		if reading.Valid {
			f := domain.Fixed(reading.Int64)
			a.Reading = &f
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- pressure traces ---------------------------------------------------------

// SaveTrace appends a compartment pressure reading.
func (t *Tx) SaveTrace(ctx context.Context, ringID string, gen domain.Generation, tr domain.PressureTrace) error {
	_, err := t.sql.ExecContext(ctx,
		"INSERT INTO pressure_traces(ring_id, generation, bay, logical_time, pressure) VALUES(?,?,?,?,?)",
		ringID, int(gen), tr.Bay, tr.LogicalTime, int64(tr.Pressure))
	return err
}

// ListTraces returns pressure points for a ring generation ordered by time.
func (t *Tx) ListTraces(ctx context.Context, ringID string, gen domain.Generation) ([]domain.PressureTrace, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT bay, logical_time, pressure FROM pressure_traces WHERE ring_id=? AND generation=? ORDER BY seq",
		ringID, int(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PressureTrace
	for rows.Next() {
		var tr domain.PressureTrace
		var p int64
		if err := rows.Scan(&tr.Bay, &tr.LogicalTime, &p); err != nil {
			return nil, err
		}
		tr.Pressure = domain.Fixed(p)
		out = append(out, tr)
	}
	return out, rows.Err()
}

// --- retests -----------------------------------------------------------------

// SaveRetest persists a deterministic retest set for a ring.
func (t *Tx) SaveRetest(ctx context.Context, ringID string, rc domain.RetestCase) error {
	affected, err := json.Marshal(rc.Affected)
	if err != nil {
		return err
	}
	resolved := 0
	if rc.Resolved {
		resolved = 1
	}
	_, err = t.sql.ExecContext(ctx,
		"INSERT OR REPLACE INTO retests(id, ring_id, generation, source, affected, resolved) VALUES(?,?,?,?,?,?)",
		rc.ID, ringID, int(rc.Generation), rc.Source, string(affected), resolved)
	return err
}

// FindRetest returns the retest case for a ring, or nil.
func (t *Tx) FindRetest(ctx context.Context, ringID string) (*domain.RetestCase, error) {
	var id, source, affected string
	var gen, resolved int
	err := t.sql.QueryRowContext(ctx,
		"SELECT id, generation, source, affected, resolved FROM retests WHERE ring_id=? ORDER BY generation DESC LIMIT 1",
		ringID).Scan(&id, &gen, &source, &affected, &resolved)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []int
	if err := json.Unmarshal([]byte(affected), &list); err != nil {
		return nil, err
	}
	return &domain.RetestCase{ID: id, Source: source, Affected: list, Generation: domain.Generation(gen), Resolved: resolved == 1}, nil
}

// MarkRetestResolved closes a retest after re-verification.
func (t *Tx) MarkRetestResolved(ctx context.Context, id string) error {
	_, err := t.sql.ExecContext(ctx, "UPDATE retests SET resolved=1 WHERE id=?", id)
	return err
}

// --- reviews -----------------------------------------------------------------

// SaveReview appends a reviewer sign-off.
func (t *Tx) SaveReview(ctx context.Context, ringID string, r domain.Review) error {
	qualified, approved := 0, 0
	if r.Qualified {
		qualified = 1
	}
	if r.Approved {
		approved = 1
	}
	_, err := t.sql.ExecContext(ctx,
		"INSERT INTO reviews(ring_id, generation, reviewer, qualified, approved) VALUES(?,?,?,?,?)",
		ringID, int(r.Generation), r.Reviewer, qualified, approved)
	return err
}

// ListReviews returns reviews for a ring generation.
func (t *Tx) ListReviews(ctx context.Context, ringID string, gen domain.Generation) ([]domain.Review, error) {
	rows, err := t.sql.QueryContext(ctx,
		"SELECT reviewer, qualified, approved FROM reviews WHERE ring_id=? AND generation=? ORDER BY seq",
		ringID, int(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Review
	for rows.Next() {
		var r domain.Review
		r.Generation = gen
		var qualified, approved int
		if err := rows.Scan(&r.Reviewer, &qualified, &approved); err != nil {
			return nil, err
		}
		r.Qualified = qualified == 1
		r.Approved = approved == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- terminal decisions -------------------------------------------------------

// SaveTerminal competes to write the single terminal decision for a ring. It
// returns the committed decision and whether this call created it. A loser
// reads the existing decision and cannot overwrite it.
func (t *Tx) SaveTerminal(ctx context.Context, ringID string, gen domain.Generation, kind, credential string) (*domain.TerminalDecision, bool, error) {
	res, err := t.sql.ExecContext(ctx,
		"INSERT OR IGNORE INTO terminals(ring_id, generation, kind, credential) VALUES(?,?,?,?)",
		ringID, int(gen), kind, credential)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		return &domain.TerminalDecision{Kind: kind, Generation: gen, Credential: credential}, true, nil
	}
	existing, err := t.FindTerminal(ctx, ringID)
	return existing, false, err
}

// FindTerminal returns the committed terminal decision for a ring, or nil.
func (t *Tx) FindTerminal(ctx context.Context, ringID string) (*domain.TerminalDecision, error) {
	var kind, credential string
	var gen int
	err := t.sql.QueryRowContext(ctx,
		"SELECT generation, kind, credential FROM terminals WHERE ring_id=?", ringID).Scan(&gen, &kind, &credential)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.TerminalDecision{Kind: kind, Generation: domain.Generation(gen), Credential: credential}, nil
}

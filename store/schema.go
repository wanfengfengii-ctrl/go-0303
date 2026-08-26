package store

import (
	"context"
	"fmt"

	"shieldtunnel/domain"
)

// schema is the full SQLite schema. It uses foreign keys and unique indexes so
// the database itself enforces concurrency atomicity: a single operation can
// never leave a partial material deduction, binding, lease, evidence or
// credential, and a competing writer observes exactly one committed winner.
const schema = `
CREATE TABLE IF NOT EXISTS events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    operation  TEXT NOT NULL,
    kind       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '',
    domain_err TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS operations (
    operation_id TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    result       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ring_tasks (
    id         TEXT PRIMARY KEY,
    section    TEXT NOT NULL,
    ring_no    INTEGER NOT NULL,
    generation INTEGER NOT NULL,
    payload    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ring_section_ring ON ring_tasks(section, ring_no);

CREATE TABLE IF NOT EXISTS gasket_bars (
    id       TEXT PRIMARY KEY,
    batch    TEXT NOT NULL,
    total_mm INTEGER NOT NULL,
    slot_seq INTEGER
);

CREATE TABLE IF NOT EXISTS material_ledger (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,
    kind      TEXT NOT NULL,
    delta_mm  INTEGER NOT NULL DEFAULT 0,
    delta_mg  INTEGER NOT NULL DEFAULT 0,
    operation TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS leases (
    resource    TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    holder      TEXT NOT NULL,
    start       INTEGER NOT NULL,
    end         INTEGER NOT NULL,
    PRIMARY KEY (resource, resource_id)
);

CREATE TABLE IF NOT EXISTS process_evidence (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    ring_id      TEXT NOT NULL,
    generation   INTEGER NOT NULL,
    kind         TEXT NOT NULL,
    logical_time INTEGER NOT NULL,
    prefix_len   INTEGER NOT NULL DEFAULT 0,
    instrument   TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);

CREATE TABLE IF NOT EXISTS device_attempts (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    ring_id      TEXT NOT NULL,
    generation   INTEGER NOT NULL,
    device_type  TEXT NOT NULL,
    call_no      INTEGER NOT NULL,
    logical_time INTEGER NOT NULL,
    retry_seq    INTEGER NOT NULL,
    fault_code   TEXT NOT NULL DEFAULT '',
    reading      INTEGER,
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);

CREATE TABLE IF NOT EXISTS pressure_traces (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    ring_id      TEXT NOT NULL,
    generation   INTEGER NOT NULL,
    bay          INTEGER NOT NULL,
    logical_time INTEGER NOT NULL,
    pressure     INTEGER NOT NULL,
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);

CREATE TABLE IF NOT EXISTS retests (
    id         TEXT PRIMARY KEY,
    ring_id    TEXT NOT NULL,
    generation INTEGER NOT NULL,
    source     TEXT NOT NULL,
    affected   TEXT NOT NULL,
    resolved   INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);

CREATE TABLE IF NOT EXISTS reviews (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    ring_id    TEXT NOT NULL,
    generation INTEGER NOT NULL,
    reviewer   TEXT NOT NULL,
    qualified  INTEGER NOT NULL,
    approved   INTEGER NOT NULL,
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);

CREATE TABLE IF NOT EXISTS terminals (
    ring_id    TEXT PRIMARY KEY,
    generation INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    credential TEXT NOT NULL,
    FOREIGN KEY (ring_id) REFERENCES ring_tasks(id)
);
`

func (d *DB) applySchema() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(schema)
	return err
}

// verify runs a startup consistency check: the append-only event log must be
// intact (contiguous sequence numbers) and every derived ring task must be
// reconstructible from its payload. A corrupted store fails loudly rather than
// serving partial state after a crash.
func (d *DB) verify(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.QueryContext(ctx, "SELECT seq FROM events ORDER BY seq")
	if err != nil {
		return err
	}
	prev := int64(0)
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			return err
		}
		if seq != prev+1 {
			rows.Close()
			return fmt.Errorf("store: event log gap at seq %d (want %d)", seq, prev+1)
		}
		prev = seq
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// Validate every stored ring task decodes and matches its indexed columns.
	trows, err := d.sql.QueryContext(ctx, "SELECT id, section, ring_no, generation, payload FROM ring_tasks")
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var id, section, payload string
		var ringNo, gen int
		if err := trows.Scan(&id, &section, &ringNo, &gen, &payload); err != nil {
			return err
		}
		task, err := decodeRingTask(payload)
		if err != nil {
			return fmt.Errorf("store: corrupt ring task %s: %w", id, err)
		}
		if task.ID != id || task.RingNo != domain.RingNo(ringNo) || task.Generation != domain.Generation(gen) {
			return fmt.Errorf("store: ring task %s index mismatch", id)
		}
	}
	return trows.Err()
}

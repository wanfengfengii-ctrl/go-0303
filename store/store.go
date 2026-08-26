// Package store defines the persistence boundary (持久化). Concrete
// implementations use SQLite with foreign keys, unique indexes and immediate
// write transactions to provide concurrency atomicity, and expose the
// append-only event log plus derived snapshot rebuild for crash recovery.
package store

import (
	"context"

	"shieldtunnel/domain"
)

// Store is the persistence boundary for the whole service. All writes are
// atomic per operation; a failed or aborted transaction leaves no visible
// partial state (no material deduction, lease, binding, evidence or credential).
type Store interface {
	// Append records one append-only event and returns its sequence number.
	Append(ctx context.Context, event Event) (int64, error)

	// LastSeq returns the highest committed event sequence number.
	LastSeq(ctx context.Context) (int64, error)

	// Replay returns committed events in sequence order from the given offset,
	// used to rebuild derived snapshots and prefixes at startup.
	Replay(ctx context.Context, from int64) ([]Event, error)

	// Close releases the underlying connection.
	Close() error
}

// Event is one append-only fact. Exactly one payload field is set per event.
type Event struct {
	Seq       int64
	Operation string
	Kind      string // lock | allocate | lease | evidence | device | trace | retest | review | decide
	Payload   any
	DomainErr *domain.Error // populated when an operation is rejected (audit only)
}

// Stable event kinds written to the append-only log.
const (
	KindLock     = "lock"
	KindAllocate = "allocate"
	KindLease    = "lease"
	KindEvidence = "evidence"
	KindDevice   = "device"
	KindTrace    = "trace"
	KindRetest   = "retest"
	KindReview   = "review"
	KindDecide   = "decide"
)

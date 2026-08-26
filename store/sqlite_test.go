package store

import (
	"context"
	"path/filepath"
	"testing"

	"shieldtunnel/domain"
)

func TestAppendAndReplayRoundTrip(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	seq1, err := db.Append(context.Background(), Event{Operation: "op-1", Kind: KindLock, Payload: map[string]any{"x": 1}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	seq2, err := db.Append(context.Background(), Event{Operation: "op-2", Kind: KindEvidence, Payload: map[string]any{"y": 2}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if seq1 != 1 || seq2 != 2 {
		t.Fatalf("seqs %d,%d want 1,2", seq1, seq2)
	}

	events, err := db.Replay(context.Background(), 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events %d want 2", len(events))
	}
	if events[0].Operation != "op-1" || events[1].Operation != "op-2" {
		t.Fatalf("unexpected replay: %+v", events)
	}
}

func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recover.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Append(context.Background(), Event{Operation: "op-1", Kind: KindLock, Payload: "a"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify the committed event is intact.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	last, err := db2.LastSeq(context.Background())
	if err != nil {
		t.Fatalf("lastseq: %v", err)
	}
	if last != 1 {
		t.Fatalf("last seq %d want 1", last)
	}
}

func TestReceiptIdempotency(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	err = db.WithTx(context.Background(), func(tx *Tx) error {
		ok, err := tx.SaveReceipt(context.Background(), "op-1", "hash-a", "result-a")
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("first save should insert")
		}
		ok, err = tx.SaveReceipt(context.Background(), "op-1", "hash-b", "result-b")
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("second save should be ignored")
		}
		rc, err := tx.FindReceipt(context.Background(), "op-1")
		if err != nil {
			return err
		}
		if rc == nil || rc.ContentHash != "hash-a" {
			t.Fatalf("receipt %+v", rc)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withtx: %v", err)
	}
}

func TestTerminalSingleWriter(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var created bool
	var existing *domain.TerminalDecision
	err = db.WithTx(context.Background(), func(tx *Tx) error {
		if err := tx.SaveRingTask(context.Background(), domain.RingTask{ID: "ring-1", Section: "s", RingNo: 1, Generation: 1}); err != nil {
			return err
		}
		d, c, err := tx.SaveTerminal(context.Background(), "ring-1", 1, "admit", "cred-1")
		if err != nil {
			return err
		}
		created = c
		d2, c2, err := tx.SaveTerminal(context.Background(), "ring-1", 1, "cancel", "")
		if err != nil {
			return err
		}
		if c2 {
			t.Fatal("second terminal should not overwrite")
		}
		existing = d2
		_ = d
		return nil
	})
	if err != nil {
		t.Fatalf("withtx: %v", err)
	}
	if !created {
		t.Fatal("first terminal should be created")
	}
	if existing == nil || existing.Kind != "admit" {
		t.Fatalf("existing terminal %+v", existing)
	}
}

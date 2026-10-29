// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// preparedSegments discovers and prepares n segments for the fake backend.
func preparedSegments(t *testing.T, h *harness, store *Store, n int, destinations ...string) ([]string, []map[string]journal.Transport) {
	t.Helper()
	ids := make([]string, 0, n)
	transports := make([]map[string]journal.Transport, 0, n)
	for i := 1; i <= n; i++ {
		id := discover(t, store, h.writeSegment(i, "segment "+itoa(i)))
		transports = append(transports, prepare(t, store, id, "fake", destinations...))
		ids = append(ids, id)
	}
	return ids, transports
}

func batchFor(id, destination string, ids []string, transports []map[string]journal.Transport) journal.Batch {
	b := journal.Batch{ID: id, Backend: "fake", Destination: destination}
	for i, segID := range ids {
		tr := transports[i][destination]
		b.Members = append(b.Members, journal.Member{SegmentID: segID, Path: tr.Path, SHA256: tr.SHA256, Bytes: tr.Bytes, Rows: tr.Rows})
	}
	return b
}

func createBatch(t *testing.T, store *Store, b journal.Batch) {
	t.Helper()
	if err := store.CreateBatch(b); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
}

func mustBatch(t *testing.T, store *Store, id string, want journal.State) journal.Batch {
	t.Helper()
	b, ok := store.Batch(id)
	if !ok {
		t.Fatalf("batch %s missing", id)
	}
	if b.State != want {
		t.Fatalf("batch %s state = %s, want %s", id, b.State, want)
	}
	return b
}

func TestCreateBatchWritesBatchBeforeMembers(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")

	var order []string
	store.hooks.beforeRename = func(file string) error {
		order = append(order, file)
		return nil
	}
	createBatch(t, store, batchFor("b1", "primary", ids, transports))

	if len(order) != 3 || order[0] != "b1.json" {
		t.Fatalf("write order = %v, want the batch file first, then 2 members", order)
	}
	store = h.restart(store)
	b := mustBatch(t, store, "b1", journal.StateSubmitting)
	if b.Backend != "fake" || b.Destination != "primary" || len(b.Members) != 2 || b.Receipt != nil || b.Attempts != 1 {
		t.Fatalf("batch = %+v, want 2 members, no receipt, attempt 1", b)
	}
	if b.Members[0].Path != transports[0]["primary"].Path || b.Members[0].SHA256 != transports[0]["primary"].SHA256 {
		t.Fatalf("member = %+v, want the transport path and sha256", b.Members[0])
	}
	for _, id := range ids {
		seg := mustState(t, store, id, journal.StateSubmitting)
		if dest := seg.Destinations["primary"]; dest.BatchID != "b1" || dest.State != journal.StateSubmitting {
			t.Fatalf("member destination = %+v, want it to point to b1", dest)
		}
	}
}

func TestCreateBatchRejectsInvalidBatches(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	discovered := discover(t, store, h.writeSegment(9, "not prepared"))
	createBatch(t, store, batchFor("taken", "primary", ids[1:], transports[1:]))

	cases := []struct {
		name  string
		batch journal.Batch
	}{
		{"no members", journal.Batch{ID: "b2", Backend: "fake", Destination: "primary"}},
		{"bad id", batchFor("../b2", "primary", ids[:1], transports[:1])},
		{"duplicate id", batchFor("taken", "primary", ids[:1], transports[:1])},
		{"member not prepared", journal.Batch{ID: "b2", Backend: "fake", Destination: "primary",
			Members: []journal.Member{{SegmentID: discovered, Path: "x"}}}},
		{"member already in a batch", batchFor("b2", "primary", ids[1:], transports[1:])},
		{"unknown destination", batchFor("b2", "legacy", ids[:1], []map[string]journal.Transport{{"legacy": transports[0]["primary"]}})},
		{"other backend", func() journal.Batch {
			b := batchFor("b2", "primary", ids[:1], transports[:1])
			b.Backend = "other"
			return b
		}()},
		{"path does not match the transport", func() journal.Batch {
			b := batchFor("b2", "primary", ids[:1], transports[:1])
			b.Members[0].Path = "/elsewhere"
			return b
		}()},
		{"member listed twice", func() journal.Batch {
			b := batchFor("b2", "primary", ids[:1], transports[:1])
			b.Members = append(b.Members, b.Members[0])
			return b
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.CreateBatch(tc.batch); err == nil {
				t.Fatalf("CreateBatch accepted %s", tc.name)
			}
		})
	}
	if exists(filepath.Join(h.cfg.StateDir, "batches", "b2.json")) {
		t.Fatal("a rejected batch was written")
	}
	mustState(t, store, ids[0], journal.StatePrepared)
}

func TestReceiptIsWrittenOnceIntoTheBatch(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))

	receipt := journal.Receipt{Backend: "fake", ID: "job-1"}
	if err := store.SetReceipt("b1", receipt); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.SetReceipt("b1", receipt); err != nil {
		t.Fatalf("SetReceipt with the same id is not idempotent: %v", err)
	}
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-2"}); !errors.Is(err, journal.ErrReceiptExists) {
		t.Fatalf("SetReceipt with another id error = %v, want ErrReceiptExists", err)
	}
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "other", ID: "job-1"}); err == nil {
		t.Fatal("SetReceipt accepted a receipt from another backend")
	}

	store = h.restart(store)
	b := mustBatch(t, store, "b1", journal.StateSubmitted)
	if b.Receipt == nil || b.Receipt.ID != "job-1" || b.Receipt.Backend != "fake" || b.SubmittedAt.IsZero() {
		t.Fatalf("batch receipt = %+v, want job-1 from fake with a submit time", b.Receipt)
	}
	for _, id := range ids {
		raw, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "segments", segmentName(indexOf(ids, id)+1)+"."+id+".json"))
		if err != nil {
			t.Fatalf("read member: %v", err)
		}
		if strings.Contains(string(raw), "job-1") {
			t.Fatal("a member entry holds the receipt; it belongs to the batch only")
		}
	}
}

func TestBatchRetryAndAmbiguityFollowTheStateMachine(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))

	next := h.clock.Now().Add(time.Minute)
	if err := store.RetryBatch("b1", next, "backend_transient"); err != nil {
		t.Fatalf("RetryBatch: %v", err)
	}
	b := mustBatch(t, store, "b1", journal.StateRetryWait)
	if !b.NextRetryAt.Equal(next) || b.LastErrorClass != "backend_transient" {
		t.Fatalf("retry state = %+v, want next %s and the error class", b, next)
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch: %v", err)
	}
	if b = mustBatch(t, store, "b1", journal.StateSubmitting); b.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", b.Attempts)
	}
	if err := store.MarkBatchAmbiguous("b1", "timeout"); err != nil {
		t.Fatalf("MarkBatchAmbiguous: %v", err)
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch from Ambiguous: %v", err)
	}
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.MarkBatchAmbiguous("b1", "temp_upload_failed"); err != nil {
		t.Fatalf("MarkBatchAmbiguous with a receipt: %v", err)
	}
	if err := store.ResumeBatch("b1"); !errors.Is(err, journal.ErrReceiptExists) {
		t.Fatalf("ResumeBatch with a receipt error = %v, want ErrReceiptExists: resubmit is a new batch", err)
	}
	// The receipt is polled after a restart; a late SUCCESS confirms it.
	if err := store.ConfirmBatch("b1"); err != nil {
		t.Fatalf("ConfirmBatch of an ambiguous batch with a receipt: %v", err)
	}
	mustState(t, store, ids[0], journal.StateConfirmed)
}

func TestABatchRemembersASubmitThatMayHaveLanded(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))

	if err := store.RetryBatch("b1", h.clock.Now(), "backend_transient"); err != nil {
		t.Fatalf("RetryBatch: %v", err)
	}
	if b := mustBatch(t, store, "b1", journal.StateRetryWait); b.MayHaveLanded {
		t.Fatal("a refused batch is marked as possibly landed")
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch: %v", err)
	}
	if err := store.MarkBatchAmbiguous("b1", "timeout"); err != nil {
		t.Fatalf("MarkBatchAmbiguous: %v", err)
	}
	if err := store.RetryBatch("b1", h.clock.Now(), "backend_transient"); err != nil {
		t.Fatalf("RetryBatch from Ambiguous: %v", err)
	}
	if b := mustBatch(t, h.restart(store), "b1", journal.StateRetryWait); !b.MayHaveLanded {
		t.Fatal("a batch whose submit may have landed lost that mark after a restart")
	}
}

func TestRetryBatchRefusesABatchWithAReceipt(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.MarkBatchAmbiguous("b1", "timeout"); err != nil {
		t.Fatalf("MarkBatchAmbiguous: %v", err)
	}
	if err := store.RetryBatch("b1", h.clock.Now(), "retained"); err != nil {
		t.Fatalf("RetryBatch of an ambiguous batch with no receipt: %v", err)
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch: %v", err)
	}
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.MarkBatchAmbiguous("b1", "job_ambiguous"); err != nil {
		t.Fatalf("MarkBatchAmbiguous with a receipt: %v", err)
	}
	if err := store.RetryBatch("b1", h.clock.Now(), "retained"); !errors.Is(err, journal.ErrReceiptExists) {
		t.Fatalf("RetryBatch with a receipt error = %v, want ErrReceiptExists: a known job is polled", err)
	}
}

func TestConfirmBatchNeedsAReceipt(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.MarkBatchAmbiguous("b1", "timeout"); err != nil {
		t.Fatalf("MarkBatchAmbiguous: %v", err)
	}
	if err := store.ConfirmBatch("b1"); !errors.Is(err, journal.ErrInvalidTransition) {
		t.Fatalf("ConfirmBatch with no receipt error = %v, want ErrInvalidTransition", err)
	}
}

func TestConfirmBatchConfirmsEveryMember(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 3, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.MarkBatchStranded("b1"); err != nil {
		t.Fatalf("MarkBatchStranded: %v", err)
	}
	// A late SUCCESS on a stranded batch still confirms it.
	if err := store.ConfirmBatch("b1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}

	store = h.restart(store)
	mustBatch(t, store, "b1", journal.StateConfirmed)
	for _, id := range ids {
		seg := mustState(t, store, id, journal.StateConfirmed)
		if seg.ConfirmedAt.IsZero() || seg.Destinations["primary"].State != journal.StateConfirmed {
			t.Fatalf("member = %+v, want confirmed with a time", seg)
		}
	}
}

func TestSettleBatchesDoesNotRewriteADestinationAlreadySettled(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary", "secondary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	createBatch(t, store, batchFor("b2", "secondary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.ConfirmBatch("b1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}
	// The segment stays in flight while its other destination is pending.
	before := mustState(t, store, ids[0], journal.StateSubmitting).UpdatedAt

	h.clock.Advance(time.Minute)
	if err := store.SettleBatches(); err != nil {
		t.Fatalf("SettleBatches: %v", err)
	}
	if after := mustState(t, store, ids[0], journal.StateSubmitting).UpdatedAt; !after.Equal(before) {
		t.Fatalf("segment rewritten at %s, want no write for a destination already settled", after)
	}
}

func TestSegmentConfirmedOnlyWhenEveryDestinationIsConfirmed(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary", "legacy")
	createBatch(t, store, batchFor("p1", "primary", ids, transports))
	createBatch(t, store, batchFor("l1", "legacy", ids, transports))
	for _, id := range []string{"p1", "l1"} {
		if err := store.SetReceipt(id, journal.Receipt{Backend: "fake", ID: "job-" + id}); err != nil {
			t.Fatalf("SetReceipt: %v", err)
		}
	}

	if err := store.ConfirmBatch("p1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}
	mustState(t, store, ids[0], journal.StateSubmitting)
	if err := store.ConfirmBatch("l1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}
	mustState(t, store, ids[0], journal.StateConfirmed)
}

func TestReleaseBatchReturnsMembersToPrepared(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.Quarantine(ids[1], "ingestion_failed"); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}

	if err := store.ReleaseBatch("b1"); err != nil {
		t.Fatalf("ReleaseBatch: %v", err)
	}
	store = h.restart(store)
	mustBatch(t, store, "b1", journal.StateReleased)
	seg := mustState(t, store, ids[0], journal.StatePrepared)
	if dest := seg.Destinations["primary"]; dest.BatchID != "" || dest.State != journal.StatePrepared {
		t.Fatalf("released member destination = %+v, want prepared with no batch", dest)
	}
	mustState(t, store, ids[1], journal.StateQuarantined)
	// The member can be batched again, alone.
	createBatch(t, store, batchFor("solo1", "primary", ids[:1], transports[:1]))
}

func indexOf(list []string, item string) int {
	for i, v := range list {
		if v == item {
			return i
		}
	}
	return -1
}

func TestReprepareResetsOnlyAnUnbatchedPreparedSegment(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids[1:], transports[1:]))

	if err := store.Reprepare(ids[0]); err != nil {
		t.Fatalf("Reprepare of an unbatched segment: %v", err)
	}
	seg := mustState(t, store, ids[0], journal.StateDiscovered)
	if len(seg.Destinations) != 0 {
		t.Fatalf("destinations = %v, want the transports dropped", seg.Destinations)
	}
	if _, err := os.Stat(transports[0]["primary"].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transport stat = %v, want it removed", err)
	}
	if err := store.Reprepare(ids[1]); !errors.Is(err, journal.ErrInvalidTransition) {
		t.Fatalf("Reprepare of a batched segment error = %v, want ErrInvalidTransition", err)
	}
	mustState(t, store, ids[1], journal.StateSubmitting)
}

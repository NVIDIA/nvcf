// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

func recoverFake(t *testing.T, store *Store) journal.Plan {
	t.Helper()
	plan, err := store.Recover(RecoverOptions{Backend: "fake"})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	return plan
}

func segmentAction(plan journal.Plan, id string) journal.Action {
	for _, a := range plan.Segments {
		if a.ID == id {
			return a.Action
		}
	}
	return journal.ActionNone
}

func batchAction(t *testing.T, plan journal.Plan, id string) journal.BatchAction {
	t.Helper()
	for _, a := range plan.Batches {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("plan has no action for batch %s: %+v", id, plan.Batches)
	return journal.BatchAction{}
}

// failNthRename stops the nth write before its rename, as a crash would.
func failNthRename(store *Store, n int) {
	count := 0
	store.hooks.beforeRename = func(string) error {
		count++
		if count == n {
			return errCrash
		}
		return nil
	}
}

func TestRecoverRedoesAnInterruptedPrepare(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))
	if err := store.BeginPrepare(id); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	partial := TransportPath(store.TransportDir(), id, "primary", ".parquet.tmp")
	if err := os.WriteFile(partial, []byte("half a file"), 0o640); err != nil {
		t.Fatalf("write partial transport: %v", err)
	}

	// F1: killed during prepare.
	store = h.restart(store)
	plan := recoverFake(t, store)

	mustState(t, store, id, journal.StateDiscovered)
	if exists(partial) {
		t.Fatal("partial transport kept after recovery")
	}
	if got := segmentAction(plan, id); got != journal.ActionPrepare || plan.PreparesReset != 1 {
		t.Fatalf("plan action = %s, resets = %d; want prepare again from the source, 1 reset", got, plan.PreparesReset)
	}
}

func TestRecoverResubmitsABatchCrashedBeforeItsReceipt(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 3, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))

	// F35 and F2: the create may have committed, but no id was saved.
	store = h.restart(store)
	plan := recoverFake(t, store)

	action := batchAction(t, plan, "b1")
	if action.Action != journal.ActionSubmit || !action.Ambiguous || action.Receipt != nil {
		t.Fatalf("batch action = %+v, want an ambiguous resubmit with no receipt", action)
	}
	b := mustBatch(t, store, "b1", journal.StateAmbiguous)
	if b.Receipt != nil {
		t.Fatal("a batch with no saved id gained a receipt")
	}
	for _, id := range ids {
		if seg := mustState(t, store, id, journal.StateSubmitting); seg.Destinations["primary"].BatchID != "b1" {
			t.Fatal("a member left the batch")
		}
	}
	if got := h.counter.Get(CounterBatchesAmbiguous); got != 1 {
		t.Fatalf("%s = %d, want 1", CounterBatchesAmbiguous, got)
	}

	store = h.restart(store)
	plan = recoverFake(t, store)
	if action := batchAction(t, plan, "b1"); action.Action != journal.ActionSubmit || !action.Ambiguous {
		t.Fatalf("second recovery = %+v, want the same resubmit", action)
	}
	if got := h.counter.Get(CounterBatchesAmbiguous); got != 1 {
		t.Fatalf("%s counted twice for one batch: %d", CounterBatchesAmbiguous, got)
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch after recovery: %v", err)
	}
}

func TestRecoverPollsABatchWithASavedReceipt(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 3, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}

	// F36 and F6: killed after the id was saved.
	store = h.restart(store)
	plan := recoverFake(t, store)

	action := batchAction(t, plan, "b1")
	if action.Action != journal.ActionPoll || action.Receipt == nil || action.Receipt.ID != "job-1" || action.Receipt.Backend != "fake" {
		t.Fatalf("batch action = %+v, want one poll of job-1 with the fake backend", action)
	}
	if len(plan.Batches) != 1 {
		t.Fatalf("plan batches = %+v, want exactly one poll for the whole batch", plan.Batches)
	}
	for _, id := range ids {
		mustState(t, store, id, journal.StateSubmitting)
		if got := segmentAction(plan, id); got != journal.ActionNone {
			t.Fatalf("member action = %s, want none: the batch drives it", got)
		}
	}
	if got := h.counter.Get(CounterBatchesAmbiguous); got != 0 {
		t.Fatalf("%s = %d, want 0", CounterBatchesAmbiguous, got)
	}
}

func TestRecoverReattachesMembersFromTheBatchFile(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	failNthRename(store, 2)
	if err := store.CreateBatch(batchFor("b1", "primary", ids, transports)); !errors.Is(err, errCrash) {
		t.Fatalf("CreateBatch error = %v, want the simulated crash after the batch file", err)
	}

	store.hooks.beforeRename = nil
	store = h.restart(store)
	plan := recoverFake(t, store)

	for _, id := range ids {
		seg := mustState(t, store, id, journal.StateSubmitting)
		if seg.Destinations["primary"].BatchID != "b1" {
			t.Fatalf("member %s not re-attached to b1", id[:8])
		}
	}
	if action := batchAction(t, plan, "b1"); action.Action != journal.ActionSubmit || !action.Ambiguous {
		t.Fatalf("batch action = %+v, want an ambiguous resubmit", action)
	}
}

func TestRecoverFinishesAnInterruptedConfirm(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	failNthRename(store, 2)
	if err := store.ConfirmBatch("b1"); !errors.Is(err, errCrash) {
		t.Fatalf("ConfirmBatch error = %v, want the simulated crash after the batch file", err)
	}

	store.hooks.beforeRename = nil
	store = h.restart(store)
	plan := recoverFake(t, store)

	for _, id := range ids {
		mustState(t, store, id, journal.StateConfirmed)
		if got := segmentAction(plan, id); got != journal.ActionCleanup {
			t.Fatalf("member action = %s, want cleanup", got)
		}
	}
	for _, a := range plan.Batches {
		if a.ID == "b1" && a.Action != journal.ActionNone {
			t.Fatalf("confirmed batch action = %+v, want none", a)
		}
	}
}

func TestRecoverKeepsRetryStateAcrossRestart(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.RetryBatch("b1", h.clock.Now().Add(time.Minute), "backend_transient"); err != nil {
		t.Fatalf("RetryBatch: %v", err)
	}
	if err := store.ResumeBatch("b1"); err != nil {
		t.Fatalf("ResumeBatch: %v", err)
	}
	next := h.clock.Now().Add(5 * time.Minute)
	if err := store.RetryBatch("b1", next, "backend_transient"); err != nil {
		t.Fatalf("RetryBatch: %v", err)
	}

	store = h.restart(store)
	plan := recoverFake(t, store)

	action := batchAction(t, plan, "b1")
	if action.Action != journal.ActionRetry || !action.NextRetryAt.Equal(next) {
		t.Fatalf("batch action = %+v, want a retry at %s", action, next)
	}
	if b := mustBatch(t, store, "b1", journal.StateRetryWait); b.Attempts != 2 || b.LastErrorClass != "backend_transient" {
		t.Fatalf("retry state = %+v, want 2 attempts and the error class", b)
	}
}

func TestCleanupStepsAndRecoveryOfAnInterruptedCleanup(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.FinishCleaning(ids[0]); !errors.Is(err, journal.ErrInvalidTransition) {
		t.Fatalf("FinishCleaning before confirm error = %v, want ErrInvalidTransition", err)
	}
	if err := store.ConfirmBatch("b1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}
	if err := store.BeginCleaning(ids[0]); err != nil {
		t.Fatalf("BeginCleaning: %v", err)
	}
	if err := store.BeginCleaning(ids[0]); err != nil {
		t.Fatalf("BeginCleaning is not repeatable: %v", err)
	}

	// F7: killed mid-cleanup.
	store = h.restart(store)
	plan := recoverFake(t, store)
	if got := segmentAction(plan, ids[0]); got != journal.ActionCleanup {
		t.Fatalf("Cleaning action = %s, want cleanup", got)
	}
	if err := store.FinishCleaning(ids[0]); err != nil {
		t.Fatalf("FinishCleaning: %v", err)
	}
	seg := mustState(t, store, ids[0], journal.StateDone)
	if seg.Reason != journal.ReasonUploaded || seg.DoneAt.IsZero() {
		t.Fatalf("done entry = %+v, want reason uploaded and a done time", seg)
	}
}

func TestEmptyAndEvictedSegmentsEndDone(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	empty := discover(t, store, h.writeSegment(1, "no records"))
	if err := store.BeginPrepare(empty); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	if err := store.MarkEmpty(empty); err != nil {
		t.Fatalf("MarkEmpty: %v", err)
	}
	if err := store.FinishEmpty(empty); err != nil {
		t.Fatalf("FinishEmpty: %v", err)
	}
	if seg := mustState(t, store, empty, journal.StateDone); seg.Reason != journal.ReasonEmpty {
		t.Fatalf("empty reason = %q", seg.Reason)
	}

	evicted := discover(t, store, h.writeSegment(2, "evicted"))
	if err := store.Evict(evicted); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if err := store.Evict(evicted); err != nil {
		t.Fatalf("Evict is not repeatable: %v", err)
	}
	if seg := mustState(t, store, evicted, journal.StateDone); seg.Reason != journal.ReasonEvicted {
		t.Fatalf("evicted reason = %q", seg.Reason)
	}
	if err := store.Evict("missing"); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("Evict of unknown id error = %v, want ErrNotFound", err)
	}
}

func TestRecoverPollsAReceiptWithItsOwnLinkedBackend(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))
	transports := prepare(t, store, id, "old", "primary")
	b := batchFor("b1", "primary", []string{id}, []map[string]journal.Transport{transports})
	b.Backend = "old"
	createBatch(t, store, b)
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "old", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}

	store = h.restart(store)
	plan, err := store.Recover(RecoverOptions{Backend: "new", Linked: func(name string) bool { return name == "old" }})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	action := batchAction(t, plan, "b1")
	if action.Action != journal.ActionPoll || action.Receipt.Backend != "old" {
		t.Fatalf("batch action = %+v, want a poll by the old backend", action)
	}
	mustState(t, store, id, journal.StateSubmitting)
}

// Dual write: the old backend is still linked. Its primary batch has a
// receipt and is polled; its legacy batch has none. Releasing the legacy
// batch must not reset the shared member while the primary poll is open.
func TestRecoverKeepsMembersHeldByAPollableBatchWhenReleasingTheirOtherBatch(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))
	transports := prepare(t, store, id, "old", "primary", "legacy")
	primary := batchFor("primary-batch", "primary", []string{id}, []map[string]journal.Transport{transports})
	primary.Backend = "old"
	createBatch(t, store, primary)
	if err := store.SetReceipt("primary-batch", journal.Receipt{Backend: "old", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	legacy := batchFor("legacy-batch", "legacy", []string{id}, []map[string]journal.Transport{transports})
	legacy.Backend = "old"
	createBatch(t, store, legacy)

	store = h.restart(store)
	plan, err := store.Recover(RecoverOptions{Backend: "new", Linked: func(name string) bool { return name == "old" }})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	mustBatch(t, store, "legacy-batch", journal.StateReleased)
	action := batchAction(t, plan, "primary-batch")
	if action.Action != journal.ActionPoll || action.Receipt.ID != "job-1" {
		t.Fatalf("primary batch action = %+v, want a poll of job-1", action)
	}
	seg, ok := store.Segment(id)
	if !ok {
		t.Fatal("segment missing")
	}
	if seg.Backend != "old" {
		t.Fatalf("segment backend = %q, want old until the primary poll settles", seg.Backend)
	}
	dest := seg.Destinations["primary"]
	if dest == nil || dest.BatchID != "primary-batch" {
		t.Fatalf("primary destination = %+v, want still held by primary-batch", dest)
	}
	if !exists(transports["primary"].Path) {
		t.Fatal("primary transport removed while its batch is still polled")
	}
	if legacyDest := seg.Destinations["legacy"]; legacyDest == nil || legacyDest.BatchID != "" {
		t.Fatalf("legacy destination = %+v, want detached from the released batch", legacyDest)
	}
	if plan.BackendResets != 0 {
		t.Fatalf("backend resets = %d, want 0 while a pollable batch holds the member", plan.BackendResets)
	}
}

func TestRecoverResetsWorkOfABackendThatIsNoLongerLinked(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	inBatch := discover(t, store, h.writeSegment(1, "segment one"))
	transports := prepare(t, store, inBatch, "old", "primary")
	b := batchFor("b1", "primary", []string{inBatch}, []map[string]journal.Transport{transports})
	b.Backend = "old"
	createBatch(t, store, b)
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "old", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.MarkBatchStranded("b1"); err != nil {
		t.Fatalf("MarkBatchStranded: %v", err)
	}
	prepared := discover(t, store, h.writeSegment(2, "segment two"))
	preparedTransports := prepare(t, store, prepared, "old", "primary")

	store = h.restart(store)
	plan, err := store.Recover(RecoverOptions{Backend: "new"})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// S3 rule 7: a receipt is never read as another backend's.
	for _, a := range plan.Batches {
		if a.Action != journal.ActionNone {
			t.Fatalf("batch action = %+v, want none for an unlinked backend", a)
		}
	}
	mustBatch(t, store, "b1", journal.StateReleased)
	for _, id := range []string{inBatch, prepared} {
		seg := mustState(t, store, id, journal.StateDiscovered)
		if seg.Backend != "" || len(seg.Destinations) != 0 {
			t.Fatalf("reset segment = %+v, want no backend and no destinations", seg)
		}
		if got := segmentAction(plan, id); got != journal.ActionPrepare {
			t.Fatalf("reset segment action = %s, want prepare for the new backend", got)
		}
	}
	if exists(transports["primary"].Path) || exists(preparedTransports["primary"].Path) {
		t.Fatal("old backend transports kept after reset")
	}
	if plan.BackendResets != 2 || h.counter.Get(CounterBackendResets) != 2 {
		t.Fatalf("backend resets = %d (counter %d), want 2", plan.BackendResets, h.counter.Get(CounterBackendResets))
	}
}

func TestRecoverRequiresTheRunningBackend(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	if _, err := store.Recover(RecoverOptions{}); err == nil {
		t.Fatal("Recover accepted no backend name")
	}
}

func TestRecoverReleasesAnUnsubmittedBatchThatLostAMember(t *testing.T) {
	cases := []struct {
		name string
		lose func(h *harness, store *Store, id string, name string) *Store
	}{
		{"evicted member", func(h *harness, store *Store, id, _ string) *Store {
			if err := store.Evict(id); err != nil {
				t.Fatalf("Evict: %v", err)
			}
			return h.restart(store)
		}},
		{"fenced member", func(h *harness, store *Store, id, name string) *Store {
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			h.corrupt(h.segmentFile(name, id), "{")
			return h.open()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			store := h.open()
			ids, transports := preparedSegments(t, h, store, 2, "primary")
			createBatch(t, store, batchFor("b1", "primary", ids, transports))

			store = tc.lose(h, store, ids[0], segmentName(1))
			plan := recoverFake(t, store)

			mustBatch(t, store, "b1", journal.StateReleased)
			for _, a := range plan.Batches {
				if a.ID == "b1" {
					t.Fatalf("batch action = %+v, want none: the batch cannot be sent with a lost member", a)
				}
			}
			seg := mustState(t, store, ids[1], journal.StatePrepared)
			if seg.Destinations["primary"].BatchID != "" || segmentAction(plan, ids[1]) != journal.ActionBatch {
				t.Fatalf("surviving member = %+v, action %s; want it batched again", seg.Destinations["primary"], segmentAction(plan, ids[1]))
			}
			if got := h.counter.Get(CounterBatchesAmbiguous); got != 1 {
				t.Fatalf("%s = %d, want 1: the batch may have been sent before the crash", CounterBatchesAmbiguous, got)
			}
		})
	}
}

func TestRecoverStillPollsABatchWithAReceiptThatLostAMember(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.Evict(ids[0]); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	store = h.restart(store)
	plan := recoverFake(t, store)
	if action := batchAction(t, plan, "b1"); action.Action != journal.ActionPoll {
		t.Fatalf("batch action = %+v, want the receipt polled", action)
	}
	mustState(t, store, ids[1], journal.StateSubmitting)
}

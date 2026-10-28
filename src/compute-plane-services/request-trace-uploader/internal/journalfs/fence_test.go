// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

func (h *harness) segmentFile(name, id string) string {
	return filepath.Join(h.cfg.StateDir, "segments", name+"."+id+".json")
}

func (h *harness) corrupt(path, content string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		h.t.Fatalf("corrupt %s: %v", filepath.Base(path), err)
	}
}

func TestUnreadableJournalIsFencedWithItsSource(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"cannot be parsed", "{not json"},
		{"unknown schema version", `{"v":2,"id":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			store := h.open()
			info := h.writeSegment(1, "segment one")
			id := discover(t, store, info)
			transports := prepare(t, store, id, "fake", "primary")
			keep := discover(t, store, h.writeSegment(2, "segment two"))
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			h.corrupt(h.segmentFile(info.Name, id), tc.content)

			// F24: restart with an unreadable journal file.
			store = h.open()

			if _, ok := store.Segment(id); ok {
				t.Fatal("unreadable entry was loaded")
			}
			mustState(t, store, keep, journal.StateDiscovered)
			fence := filepath.Join(h.cfg.StateDir, "unreadable", info.Name+"."+id)
			for _, moved := range []string{
				filepath.Join(fence, info.Name+"."+id+".json"),
				filepath.Join(fence, info.Name),
				filepath.Join(fence, filepath.Base(transports["primary"].Path)),
			} {
				if !exists(moved) {
					t.Errorf("%s not fenced", filepath.Base(moved))
				}
			}
			if exists(filepath.Join(h.cfg.SpoolDir, info.Name)) {
				t.Fatal("source left in the spool, so it would be discovered and submitted again")
			}
			if got := h.counter.Get(CounterUnreadable); got != 1 {
				t.Fatalf("%s = %d, want 1", CounterUnreadable, got)
			}

			store = h.restart(store)
			if got := len(store.Segments()); got != 1 {
				t.Fatalf("segments after a second restart = %d, want 1", got)
			}
		})
	}
}

func TestFencingTheSameNameTwiceKeepsBoth(t *testing.T) {
	h := newHarness(t)
	for _, content := range []string{"first lifetime", "second lifetime"} {
		store := h.open()
		info := h.writeSegment(1, content)
		id := discover(t, store, info)
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		h.corrupt(h.segmentFile(info.Name, id), "{")
		if err := h.open().Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	fenced, err := os.ReadDir(filepath.Join(h.cfg.StateDir, "unreadable"))
	if err != nil || len(fenced) != 2 {
		t.Fatalf("fenced entries = %v, %v; want two directories", fenced, err)
	}
}

func TestUnidentifiableJournalStopsTheSpool(t *testing.T) {
	id := strings.Repeat("a", 64)
	cases := []struct {
		name string
		file string
	}{
		{"no segment name or id", "not-a-journal-name.json"},
		{"segment name without an id", "request-trace.000001.jsonl.gz.json"},
		{"short id", "request-trace.000001.jsonl.gz.abc123.json"},
		{"id without a segment name", "." + id + ".json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if err := h.open().Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			stray := filepath.Join(h.cfg.StateDir, "segments", tc.file)
			h.corrupt(stray, "{")

			_, err := Open(t.Context(), h.cfg, Options{Counter: h.counter})
			if !errors.Is(err, journal.ErrUnreadableState) {
				t.Fatalf("Open error = %v, want ErrUnreadableState", err)
			}
			if got := journal.NotReadyReason(err); got != "unreadable_state" {
				t.Fatalf("NotReadyReason = %q, want unreadable_state", got)
			}
			if !exists(stray) {
				t.Fatal("an unidentifiable file was moved; the spool must stop without guessing")
			}
			if got := h.counter.Get(CounterUnreadable); got != 0 {
				t.Fatalf("%s = %d, want 0: nothing was fenced", CounterUnreadable, got)
			}
		})
	}
}

// An entry whose name identifies its source, but whose source is gone,
// protects nothing that could be uploaded again, so it is fenced alone and
// the spool keeps working.
func TestUnreadableJournalWithAMissingSourceIsFencedAlone(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	transports := prepare(t, store, id, "fake", "primary")
	keep := discover(t, store, h.writeSegment(2, "segment two"))
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.corrupt(h.segmentFile(info.Name, id), "{")
	if err := os.Remove(filepath.Join(h.cfg.SpoolDir, info.Name)); err != nil {
		t.Fatalf("remove source: %v", err)
	}

	store = h.open()

	fence := filepath.Join(h.cfg.StateDir, "unreadable", info.Name+"."+id)
	if !exists(filepath.Join(fence, info.Name+"."+id+".json")) || exists(h.segmentFile(info.Name, id)) {
		t.Fatal("journal file not fenced")
	}
	if !exists(filepath.Join(fence, filepath.Base(transports["primary"].Path))) {
		t.Fatal("transport not fenced with its journal file")
	}
	if got := h.counter.Get(CounterUnreadable); got != 1 {
		t.Fatalf("%s = %d, want 1", CounterUnreadable, got)
	}
	if _, ok := store.Segment(id); ok {
		t.Fatal("unreadable entry was loaded")
	}
	mustState(t, store, keep, journal.StateDiscovered)
	if _, err := store.Recover(RecoverOptions{Backend: "fake"}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
}

func TestUnreadableBatchIsFencedAndItsMembersAreBatchedAgain(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 2, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.corrupt(filepath.Join(h.cfg.StateDir, "batches", "b1.json"), "{")

	store = h.open()
	if _, ok := store.Batch("b1"); ok {
		t.Fatal("unreadable batch was loaded")
	}
	if !exists(filepath.Join(h.cfg.StateDir, "unreadable", "batches", "b1.json")) {
		t.Fatal("unreadable batch file not fenced")
	}
	plan := recoverFake(t, store)
	for _, id := range ids {
		seg := mustState(t, store, id, journal.StatePrepared)
		if seg.Destinations["primary"].BatchID != "" {
			t.Fatal("member still points to the fenced batch")
		}
		if got := segmentAction(plan, id); got != journal.ActionBatch {
			t.Fatalf("member action = %s, want batch again", got)
		}
	}
	if got := h.counter.Get(CounterUnreadable); got != 1 {
		t.Fatalf("%s = %d, want 1", CounterUnreadable, got)
	}
	if got := h.counter.Get(CounterBatchesAmbiguous); got != 1 {
		t.Fatalf("%s = %d, want 1: the fenced batch may have been submitted", CounterBatchesAmbiguous, got)
	}
}

func TestTombstonesAreKeptForTheTTLThenSwept(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	createBatch(t, store, batchFor("b1", "primary", ids, transports))
	if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
		t.Fatalf("SetReceipt: %v", err)
	}
	if err := store.ConfirmBatch("b1"); err != nil {
		t.Fatalf("ConfirmBatch: %v", err)
	}
	if err := store.BeginCleaning(ids[0]); err != nil {
		t.Fatalf("BeginCleaning: %v", err)
	}
	if err := store.FinishCleaning(ids[0]); err != nil {
		t.Fatalf("FinishCleaning: %v", err)
	}
	live := discover(t, store, h.writeSegment(2, "still pending"))

	h.clock.Advance(23 * time.Hour)
	if removed, err := store.Sweep(); err != nil || removed != 0 {
		t.Fatalf("Sweep before the TTL = %d, %v; want nothing removed", removed, err)
	}
	mustState(t, store, ids[0], journal.StateDone)

	h.clock.Advance(2 * time.Hour)
	removed, err := store.Sweep()
	if err != nil || removed != 2 {
		t.Fatalf("Sweep after the TTL = %d, %v; want the tombstone and its batch removed", removed, err)
	}
	if _, ok := store.Segment(ids[0]); ok {
		t.Fatal("tombstone kept after the TTL")
	}
	if _, ok := store.Batch("b1"); ok {
		t.Fatal("finished batch kept after the TTL")
	}
	mustState(t, store, live, journal.StateDiscovered)
	store = h.restart(store)
	if got := len(store.Segments()); got != 1 {
		t.Fatalf("segments after restart = %d, want only the pending one", got)
	}
}

func TestInterruptedFenceIsFinishedAtTheNextStart(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.corrupt(h.segmentFile(info.Name, id), "{")
	// A crash after the source moved into the fence and before the journal
	// file followed it.
	fence := filepath.Join(h.cfg.StateDir, "unreadable", info.Name+"."+id)
	if err := os.MkdirAll(fence, 0o750); err != nil {
		t.Fatalf("create fence: %v", err)
	}
	if err := os.Rename(filepath.Join(h.cfg.SpoolDir, info.Name), filepath.Join(fence, info.Name)); err != nil {
		t.Fatalf("move source: %v", err)
	}

	h.open()
	if !exists(filepath.Join(fence, info.Name+"."+id+".json")) || exists(h.segmentFile(info.Name, id)) {
		t.Fatal("the interrupted fence was not finished")
	}
}

// rewrite edits a journal file as JSON, keeping it parseable.
func (h *harness) rewrite(path string, edit func(map[string]any)) {
	h.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		h.t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
	edit(record)
	out, err := json.Marshal(record)
	if err != nil {
		h.t.Fatalf("encode: %v", err)
	}
	h.corrupt(path, string(out))
}

func destination(record map[string]any) map[string]any {
	return record["destinations"].(map[string]any)["primary"].(map[string]any)
}

func TestParseableButInvalidSegmentEntriesAreFenced(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown state", func(r map[string]any) { r["state"] = "bogus" }},
		{"missing state", func(r map[string]any) { delete(r, "state") }},
		{"batch-only state", func(r map[string]any) { r["state"] = "submitted" }},
		{"null destination", func(r map[string]any) { r["destinations"] = map[string]any{"primary": nil} }},
		{"prepared without destinations", func(r map[string]any) { delete(r, "destinations") }},
		{"prepared without a backend", func(r map[string]any) { delete(r, "backend") }},
		{"unknown destination state", func(r map[string]any) { destination(r)["state"] = "bogus" }},
		{"transport outside the transport directory", func(r map[string]any) {
			destination(r)["transport"].(map[string]any)["path"] = "/etc/passwd"
		}},
		{"unsafe batch id", func(r map[string]any) { destination(r)["batch_id"] = "../b1" }},
		{"bad content hash", func(r map[string]any) { r["sha256"] = "zz" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			store := h.open()
			info := h.writeSegment(1, "segment one")
			id := discover(t, store, info)
			prepare(t, store, id, "fake", "primary")
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			h.rewrite(h.segmentFile(info.Name, id), tc.edit)

			store = h.open()
			if _, ok := store.Segment(id); ok {
				t.Fatal("invalid entry was loaded")
			}
			if got := h.counter.Get(CounterUnreadable); got != 1 {
				t.Fatalf("%s = %d, want 1", CounterUnreadable, got)
			}
			if exists(filepath.Join(h.cfg.SpoolDir, info.Name)) {
				t.Fatal("source of an invalid entry left in the spool")
			}
			if _, err := store.Recover(RecoverOptions{Backend: "fake"}); err != nil {
				t.Fatalf("Recover after fencing: %v", err)
			}
		})
	}
}

func TestParseableButInvalidBatchEntriesAreFenced(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown state", func(r map[string]any) { r["state"] = "bogus" }},
		{"no members", func(r map[string]any) { r["members"] = []any{} }},
		{"null member list", func(r map[string]any) { r["members"] = nil }},
		{"member path outside the transport directory", func(r map[string]any) {
			r["members"].([]any)[0].(map[string]any)["path"] = "/etc/passwd"
		}},
		{"receipt from another backend", func(r map[string]any) {
			r["receipt"].(map[string]any)["backend"] = "other"
		}},
		{"submitted without a receipt", func(r map[string]any) { delete(r, "receipt") }},
		{"no destination", func(r map[string]any) { r["destination"] = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			store := h.open()
			ids, transports := preparedSegments(t, h, store, 1, "primary")
			createBatch(t, store, batchFor("b1", "primary", ids, transports))
			if err := store.SetReceipt("b1", journal.Receipt{Backend: "fake", ID: "job-1"}); err != nil {
				t.Fatalf("SetReceipt: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			h.rewrite(filepath.Join(h.cfg.StateDir, "batches", "b1.json"), tc.edit)

			store = h.open()
			if _, ok := store.Batch("b1"); ok {
				t.Fatal("invalid batch was loaded")
			}
			if got := h.counter.Get(CounterUnreadable); got != 1 {
				t.Fatalf("%s = %d, want 1", CounterUnreadable, got)
			}
		})
	}
}

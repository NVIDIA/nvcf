// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

type recorded struct {
	kind, path string
}

func record(store *Store) *[]recorded {
	var events []recorded
	store.hooks.event = func(kind, path string) { events = append(events, recorded{kind, path}) }
	return &events
}

func indexOfEvent(events []recorded, kind, path string) int {
	return slices.IndexFunc(events, func(e recorded) bool { return e.kind == kind && e.path == path })
}

func TestAtomicWriteSyncsTheFileBeforeRenameAndTheDirectoryAfter(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	events := record(store)

	if err := store.BeginPrepare(id); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	final := h.segmentFile(info.Name, id)
	want := []recorded{
		{"sync-file", final + ".tmp"},
		{"rename", final},
		{"sync-dir", filepath.Dir(final)},
	}
	if !slices.Equal(*events, want) {
		t.Fatalf("write steps = %v, want %v", *events, want)
	}
}

func TestFailedDirectorySyncIsReportedAndTheWriteCanBeRepeated(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))

	store.hooks.beforeSyncDir = func(string) error { return errCrash }
	if err := store.BeginPrepare(id); !errors.Is(err, errCrash) {
		t.Fatalf("BeginPrepare error = %v, want the directory sync failure", err)
	}
	store.hooks.beforeSyncDir = nil
	if err := store.BeginPrepare(id); err != nil {
		t.Fatalf("repeated BeginPrepare: %v", err)
	}
	store = h.restart(store)
	mustState(t, store, id, journal.StatePreparing)
}

func TestNewStateDirectoriesAreLinkedDurably(t *testing.T) {
	h := newHarness(t)
	var events []recorded
	store, err := openWithHooks(h, hooks{event: func(kind, path string) { events = append(events, recorded{kind, path}) }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, parent := range []string{h.cfg.SpoolDir, h.cfg.StateDir} {
		if indexOfEvent(events, "sync-dir", parent) < 0 {
			t.Errorf("parent %s not synced after creating state directories", filepath.Base(parent))
		}
	}
}

func TestFenceLinksItsDirectoryBeforeMovingFilesIntoIt(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.corrupt(h.segmentFile(info.Name, id), "{")

	var events []recorded
	store, err := openWithHooks(h, hooks{event: func(kind, path string) { events = append(events, recorded{kind, path}) }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	unreadable := filepath.Join(h.cfg.StateDir, "unreadable")
	fence := filepath.Join(unreadable, info.Name+"."+id)
	linked := indexOfEvent(events, "sync-dir", unreadable)
	moved := indexOfEvent(events, "rename", filepath.Join(fence, info.Name))
	fenceSynced := indexOfEvent(events, "sync-dir", fence)
	spoolSynced := indexOfEvent(events, "sync-dir", h.cfg.SpoolDir)
	if linked < 0 || moved < 0 || fenceSynced < 0 || spoolSynced < 0 {
		t.Fatalf("fence steps missing: %v", events)
	}
	if !(linked < moved && moved < fenceSynced && fenceSynced < spoolSynced) {
		t.Fatalf("fence steps out of order: %v", events)
	}
}

func TestResumedFenceKeepsTheFencedSourceWhenTheNameIsReused(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "first lifetime")
	id := discover(t, store, info)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.corrupt(h.segmentFile(info.Name, id), "{")
	fence := filepath.Join(h.cfg.StateDir, "unreadable", info.Name+"."+id)
	if err := os.MkdirAll(fence, 0o750); err != nil {
		t.Fatalf("create fence: %v", err)
	}
	if err := os.Rename(filepath.Join(h.cfg.SpoolDir, info.Name), filepath.Join(fence, info.Name)); err != nil {
		t.Fatalf("move source: %v", err)
	}
	// Dynamo wrote a new segment under the same name before the restart.
	h.writeSegment(1, "second lifetime")

	h.open()
	fenced, err := os.ReadFile(filepath.Join(fence, info.Name))
	if err != nil || string(fenced) != "first lifetime" {
		t.Fatalf("fenced source = %q, %v; want the first lifetime kept", fenced, err)
	}
	spool, err := os.ReadFile(filepath.Join(h.cfg.SpoolDir, info.Name))
	if err != nil || string(spool) != "second lifetime" {
		t.Fatalf("spool source = %q, %v; want the new segment left for upload", spool, err)
	}
	if !exists(filepath.Join(fence, info.Name+"."+id+".json")) {
		t.Fatal("journal file not moved into the fence")
	}
}

func openWithHooks(h *harness, hk hooks) (*Store, error) {
	return openStore(h.t.Context(), h.cfg, Options{Now: h.clock.Now, Counter: h.counter}, hk)
}

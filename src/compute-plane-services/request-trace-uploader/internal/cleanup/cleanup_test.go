// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cleanup_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
)

// fakeJournal records the cleanup steps in order. It also lists, at each
// journal step, which files still exist, so the tests can check the order of
// journal writes and deletes.
type fakeJournal struct {
	steps   []string
	watch   []string
	failAt  string
	counts  map[string]int64
	cleaned map[string]bool
}

func (j *fakeJournal) step(name, id string) error {
	present := make([]string, 0, len(j.watch))
	for _, path := range j.watch {
		if _, err := os.Stat(path); err == nil {
			present = append(present, filepath.Base(path))
		}
	}
	j.steps = append(j.steps, name+"["+strings.Join(present, ",")+"]")
	if name == j.failAt {
		return errors.New("simulated crash")
	}
	return nil
}

func (j *fakeJournal) BeginCleaning(id string) error  { return j.step("begin", id) }
func (j *fakeJournal) FinishCleaning(id string) error { return j.step("done", id) }
func (j *fakeJournal) FinishEmpty(id string) error    { return j.step("empty", id) }

// fakeSpool answers the highest index. Cleanup asks after deleting the
// transports and before touching the source, so the fake records which
// files exist at that moment.
type fakeSpool struct {
	highest int
	found   bool
	err     error
	watch   []string
	seen    *[]string
}

func (s fakeSpool) HighestIndex(string) (int, bool, error) {
	if s.seen != nil {
		for _, path := range s.watch {
			if _, err := os.Stat(path); err == nil {
				*s.seen = append(*s.seen, filepath.Base(path))
			}
		}
	}
	return s.highest, s.found, s.err
}

type counts map[string]int64

func (c counts) Add(name string, delta int64) { c[name] += delta }

type hashCounter struct{ calls int }

func (h *hashCounter) hash(path string) (string, error) {
	h.calls++
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type fixture struct {
	t       *testing.T
	item    cleanup.Item
	journal *fakeJournal
	counter counts
	hasher  *hashCounter
	spool   fakeSpool
}

func newFixture(t *testing.T, content string, transports int) *fixture {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "request-trace.000004.jsonl.gz")
	if err := os.WriteFile(source, []byte(content), 0o640); err != nil {
		t.Fatalf("write source: %v", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	item := cleanup.Item{
		SegmentID: "seg-4",
		Source:    source,
		Prefix:    "request-trace",
		Index:     4,
		Size:      info.Size(),
		ModTime:   info.ModTime(),
		SHA256:    hex.EncodeToString(sum[:]),
	}
	for i := range transports {
		path := filepath.Join(dir, "transport-"+string(rune('a'+i)))
		if err := os.WriteFile(path, []byte("parquet"), 0o640); err != nil {
			t.Fatalf("write transport: %v", err)
		}
		item.Transports = append(item.Transports, path)
	}
	return &fixture{
		t:       t,
		item:    item,
		journal: &fakeJournal{watch: append([]string{source}, item.Transports...)},
		counter: counts{},
		hasher:  &hashCounter{},
		spool:   fakeSpool{highest: 9, found: true},
	}
}

func (f *fixture) cleaner() *cleanup.Cleaner {
	return cleanup.New(f.journal, f.spool, cleanup.Options{Counter: f.counter, Hash: f.hasher.hash})
}

func TestCleanRunsTheStepsInOrder(t *testing.T) {
	f := newFixture(t, "uploaded records", 2)
	var seen []string
	f.spool.watch, f.spool.seen = f.journal.watch, &seen

	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("Clean = %s, %v; want done", result, err)
	}
	want := "begin[request-trace.000004.jsonl.gz,transport-a,transport-b]|done[]"
	if got := strings.Join(f.journal.steps, "|"); got != want {
		t.Fatalf("steps = %s, want %s", got, want)
	}
	if got := strings.Join(seen, ","); got != "request-trace.000004.jsonl.gz" {
		t.Fatalf("files present when the source was checked = %s, want only the source: transports go first", got)
	}
	if f.hasher.calls != 0 {
		t.Fatalf("re-hashed an unchanged source %d times, want 0", f.hasher.calls)
	}
	if f.counter[cleanup.CounterSegmentsDone] != 1 {
		t.Fatalf("%s = %d, want 1", cleanup.CounterSegmentsDone, f.counter[cleanup.CounterSegmentsDone])
	}
}

func TestCleanResumesAfterACrashBetweenTransportAndSourceDelete(t *testing.T) {
	f := newFixture(t, "uploaded records", 2)
	// F7: the transports were deleted, then the process was killed.
	for _, path := range f.item.Transports {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove transport: %v", err)
		}
	}

	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("Clean = %s, %v; want done", result, err)
	}
	if _, err := os.Stat(f.item.Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source kept after a resumed cleanup")
	}
}

func TestCleanFinishesWhenTheSourceIsAlreadyGone(t *testing.T) {
	f := newFixture(t, "uploaded records", 1)
	// Killed after the source delete and before Done was recorded.
	if err := os.Remove(f.item.Source); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone || !strings.HasPrefix(f.journal.steps[len(f.journal.steps)-1], "done") {
		t.Fatalf("Clean = %s, %v, steps %v; want done recorded", result, err, f.journal.steps)
	}
}

func TestCleanKeepsASourceThatChanged(t *testing.T) {
	f := newFixture(t, "uploaded records", 1)
	// F8: the file under that name changed after it was uploaded.
	if err := os.WriteFile(f.item.Source, []byte("different records!"), 0o640); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	result, err := f.cleaner().Clean(t.Context(), f.item)
	if !errors.Is(err, cleanup.ErrSourceChanged) || result == cleanup.ResultDone {
		t.Fatalf("Clean = %s, %v; want ErrSourceChanged", result, err)
	}
	if _, err := os.Stat(f.item.Source); err != nil {
		t.Fatal("changed source was deleted")
	}
	if f.hasher.calls != 1 || f.counter[cleanup.CounterSourceChanged] != 1 {
		t.Fatalf("hash calls = %d, %s = %d; want 1 and 1", f.hasher.calls, cleanup.CounterSourceChanged, f.counter[cleanup.CounterSourceChanged])
	}
	for _, step := range f.journal.steps {
		if strings.HasPrefix(step, "done") {
			t.Fatal("Done recorded for a changed source")
		}
	}
}

func TestCleanDeletesASourceWhoseMtimeChangedButContentDidNot(t *testing.T) {
	f := newFixture(t, "uploaded records", 0)
	later := f.item.ModTime.Add(time.Hour)
	if err := os.Chtimes(f.item.Source, later, later); err != nil {
		t.Fatalf("touch source: %v", err)
	}

	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone || f.hasher.calls != 1 {
		t.Fatalf("Clean = %s, %v with %d hash calls; want done after one re-hash", result, err, f.hasher.calls)
	}
}

func TestCleanNeverDeletesTheHighestIndex(t *testing.T) {
	f := newFixture(t, "uploaded records", 1)
	f.spool = fakeSpool{highest: f.item.Index, found: true}

	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDeferred {
		t.Fatalf("Clean = %s, %v; want deferred", result, err)
	}
	if _, err := os.Stat(f.item.Source); err != nil {
		t.Fatal("highest-index source was deleted")
	}
	for _, step := range f.journal.steps {
		if strings.HasPrefix(step, "done") {
			t.Fatal("Done recorded while the source is kept")
		}
	}
}

func TestCleanStopsWhenTheJournalCannotRecordCleaning(t *testing.T) {
	f := newFixture(t, "uploaded records", 1)
	f.journal.failAt = "begin"
	if _, err := f.cleaner().Clean(t.Context(), f.item); err == nil {
		t.Fatal("Clean went on after the journal failed")
	}
	if _, err := os.Stat(f.item.Transports[0]); err != nil {
		t.Fatal("a transport was deleted before Cleaning was recorded")
	}
}

func TestCleanEmptyRemovesTheSourceAndCounts(t *testing.T) {
	f := newFixture(t, "", 0)

	// F20: a zero-length segment is removed with no upload.
	result, err := f.cleaner().CleanEmpty(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("CleanEmpty = %s, %v; want done", result, err)
	}
	if got := strings.Join(f.journal.steps, "|"); got != "empty[]" {
		t.Fatalf("steps = %s, want empty recorded after the source is gone", got)
	}
	if f.counter[cleanup.CounterSegmentsEmpty] != 1 {
		t.Fatalf("%s = %d, want 1", cleanup.CounterSegmentsEmpty, f.counter[cleanup.CounterSegmentsEmpty])
	}
}

func TestCleanEmptyNeverDeletesTheHighestIndex(t *testing.T) {
	f := newFixture(t, "", 0)
	f.spool = fakeSpool{highest: f.item.Index, found: true}
	result, err := f.cleaner().CleanEmpty(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDeferred || len(f.journal.steps) != 0 {
		t.Fatalf("CleanEmpty = %s, %v, steps %v; want deferred with nothing recorded", result, err, f.journal.steps)
	}
	if _, err := os.Stat(f.item.Source); err != nil {
		t.Fatal("highest-index empty source was deleted")
	}
}

func TestCleanRejectsAnIncompleteItem(t *testing.T) {
	f := newFixture(t, "uploaded records", 0)
	item := f.item
	item.SHA256 = ""
	if _, err := f.cleaner().Clean(t.Context(), item); err == nil {
		t.Fatal("Clean accepted an item with no fingerprint")
	}
	if len(f.journal.steps) != 0 {
		t.Fatalf("journal written for an invalid item: %v", f.journal.steps)
	}
}

func TestCleanStoppedBetweenTransportAndSourceDeleteResumes(t *testing.T) {
	f := newFixture(t, "uploaded records", 2)
	f.spool.err = errors.New("spool listing failed")

	if _, err := f.cleaner().Clean(t.Context(), f.item); err == nil {
		t.Fatal("Clean went on without knowing the highest index")
	}
	for _, path := range f.item.Transports {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a transport is still present after the first step")
		}
	}
	if _, err := os.Stat(f.item.Source); err != nil {
		t.Fatal("the source was deleted although the highest index was unknown")
	}

	f.spool.err = nil
	result, err := f.cleaner().Clean(t.Context(), f.item)
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("resumed Clean = %s, %v; want done", result, err)
	}
}

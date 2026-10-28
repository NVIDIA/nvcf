// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/shadow"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

const prefix = "request-trace"

type spoolView struct {
	cfg shadow.Config
}

func newSpoolView(t *testing.T) spoolView {
	t.Helper()
	return spoolView{cfg: shadow.Config{SourceDir: t.TempDir(), Destination: "shadow_table"}.WithDefaults()}
}

func (v spoolView) seg(index int) segment.Segment {
	return segment.Segment{
		Path:   filepath.Join(v.cfg.SourceDir, fmt.Sprintf("%s.%06d.jsonl.gz", prefix, index)),
		Prefix: prefix,
		Index:  index,
	}
}

// scan lists the spool as intake would: the last index is the active one.
func (v spoolView) scan(indexes ...int) segment.Scan {
	var s segment.Scan
	for i, index := range indexes {
		if i == len(indexes)-1 {
			s.Active = append(s.Active, v.seg(index))
			continue
		}
		s.Closed = append(s.Closed, v.seg(index))
	}
	return s
}

func newTracker(t *testing.T, cfg shadow.Config) (*shadow.Tracker, *telemetry.Memory) {
	t.Helper()
	metrics, err := telemetry.NewMemory("test")
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := shadow.NewTracker(cfg, metrics)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	return tracker, metrics
}

func missed(t *testing.T, metrics *telemetry.Memory) float64 {
	t.Helper()
	value, ok := metrics.Value(telemetry.SegmentsMissed, nil)
	if !ok {
		t.Fatalf("%s has no series", telemetry.SegmentsMissed)
	}
	return value
}

func observe(t *testing.T, tracker *shadow.Tracker, scan segment.Scan) int {
	t.Helper()
	n, err := tracker.Observe(scan)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	return n
}

// goneErr is what prepare returns when the owner deleted the source first.
func goneErr(path string) error {
	_, err := os.Open(path)
	return errors.Join(fmt.Errorf("open segment: %w", err), errors.New("abort: nothing to discard"))
}

func read(t *testing.T, tracker *shadow.Tracker, seg segment.Segment) {
	t.Helper()
	if missed, err := tracker.Track(seg, func() error { return nil }); missed || err != nil {
		t.Fatalf("Track() = %v, %v for a readable source", missed, err)
	}
}

// gone tracks a read that finds the source already deleted by the owner.
func gone(t *testing.T, tracker *shadow.Tracker, seg segment.Segment) bool {
	t.Helper()
	missed, err := tracker.Track(seg, func() error { return goneErr(seg.Path) })
	if err != nil {
		t.Fatalf("Track() error = %v for a source the owner deleted", err)
	}
	return missed
}

func TestNewTrackerPresetsMissedToZero(t *testing.T) {
	_, metrics := newTracker(t, newSpoolView(t).cfg)
	if got := missed(t, metrics); got != 0 {
		t.Fatalf("%s = %v, want 0", telemetry.SegmentsMissed, got)
	}
}

func TestNewTrackerRejectsInvalidConfig(t *testing.T) {
	metrics, _ := telemetry.NewMemory("test")
	if _, err := shadow.NewTracker(shadow.Config{SourceDir: "spool", Destination: "x"}, metrics); err == nil {
		t.Fatal("NewTracker() error = nil for a relative source")
	}
	if _, err := shadow.NewTracker(newSpoolView(t).cfg, nil); err == nil {
		t.Fatal("NewTracker() error = nil without metrics")
	}
}

func TestIsGone(t *testing.T) {
	path := "/spool/request-trace.000001.jsonl.gz"
	openGone := &fs.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"source gone at open", fmt.Errorf("open segment: %w", openGone), true},
		{"source gone, joined with an abort error", errors.Join(fmt.Errorf("open segment: %w", openGone), errors.New("abort")), true},
		{"another file gone", &fs.PathError{Op: "open", Path: "/state/transport/x.tmp", Err: syscall.ENOENT}, false},
		{"source not readable", &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}, false},
		{"source gone during a later step", &fs.PathError{Op: "stat", Path: path, Err: syscall.ENOENT}, false},
		{"plain error", errors.New("disk full"), false},
		{"no error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shadow.IsGone(tt.err, path); got != tt.want {
				t.Fatalf("IsGone() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrackCountsAnUnreadSourceOnce(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1, 2))

	if !gone(t, tracker, v.seg(1)) {
		t.Fatal("Track() missed = false for a source deleted before it was read")
	}
	if !gone(t, tracker, v.seg(1)) {
		t.Fatal("Track() missed = false on a second attempt")
	}
	if got := observe(t, tracker, v.scan(2)); got != 0 {
		t.Fatalf("Observe() = %d, want the missed segment not counted again", got)
	}
	if got := missed(t, metrics); got != 1 {
		t.Fatalf("%s = %v, want 1", telemetry.SegmentsMissed, got)
	}
}

func TestTrackReturnsOtherErrors(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1, 2))
	missedIt, err := tracker.Track(v.seg(1), func() error { return errors.New("disk full") })
	if missedIt || err == nil {
		t.Fatalf("Track() = %v, %v, want the error back and nothing missed", missedIt, err)
	}
	if got := missed(t, metrics); got != 0 {
		t.Fatalf("%s = %v, want 0", telemetry.SegmentsMissed, got)
	}
}

func TestObserveCountsUnreadSegmentsThatVanish(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	if got := observe(t, tracker, v.scan(1, 2, 3)); got != 0 {
		t.Fatalf("first Observe() = %d, want 0", got)
	}
	read(t, tracker, v.seg(1))

	if got := observe(t, tracker, v.scan(3)); got != 1 {
		t.Fatalf("Observe() = %d, want 1: segment 2 left unread, segment 1 was read", got)
	}
	if got := missed(t, metrics); got != 1 {
		t.Fatalf("%s = %v, want 1", telemetry.SegmentsMissed, got)
	}
}

func TestObserveCountsIndexesThatCameAndWentBetweenScans(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1))
	read(t, tracker, v.seg(1))

	if got := observe(t, tracker, v.scan(2, 5)); got != 2 {
		t.Fatalf("Observe() = %d, want 2 for indexes 3 and 4", got)
	}
	read(t, tracker, v.seg(2))
	if got := observe(t, tracker, v.scan(5)); got != 0 {
		t.Fatalf("Observe() = %d, want 0", got)
	}
	if got := missed(t, metrics); got != 2 {
		t.Fatalf("%s = %v, want 2", telemetry.SegmentsMissed, got)
	}
}

func TestFirstScanDoesNotCountGapsFromBeforeTheShadowStarted(t *testing.T) {
	v := newSpoolView(t)
	tracker, _ := newTracker(t, v.cfg)
	if got := observe(t, tracker, v.scan(2, 5, 9)); got != 0 {
		t.Fatalf("first Observe() = %d, want 0", got)
	}
}

// Intake hands segments to the sink during its pass, before the service
// reports the scan, so the sink can read or miss a segment the tracker has
// not seen yet.
func TestReadOrGoneBeforeObserveCountsOnce(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1))

	read(t, tracker, v.seg(2))
	if !gone(t, tracker, v.seg(3)) {
		t.Fatal("Track() missed = false")
	}
	if got := observe(t, tracker, v.scan(1, 2, 3, 4)); got != 0 {
		t.Fatalf("Observe() = %d, want 0", got)
	}
	read(t, tracker, v.seg(1))
	if got := observe(t, tracker, v.scan(4)); got != 0 {
		t.Fatalf("Observe() = %d, want 0: 2 was read and 3 already counted", got)
	}
	if got := missed(t, metrics); got != 1 {
		t.Fatalf("%s = %v, want 1", telemetry.SegmentsMissed, got)
	}
}

func TestRestartedNumberingIsNotCountedAsMissed(t *testing.T) {
	v := newSpoolView(t)
	tracker, _ := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(8, 9, 10))
	for _, index := range []int{8, 9, 10} {
		read(t, tracker, v.seg(index))
	}

	if got := observe(t, tracker, v.scan(0, 1)); got != 0 {
		t.Fatalf("Observe() = %d after the writer restarted at 0, want 0", got)
	}
	read(t, tracker, v.seg(0))
	read(t, tracker, v.seg(1))
	if got := observe(t, tracker, v.scan(4)); got != 2 {
		t.Fatalf("Observe() = %d, want 2 for indexes 2 and 3 of the new numbering", got)
	}
}

func TestTrackerStateSurvivesRestart(t *testing.T) {
	v := newSpoolView(t)
	first, _ := newTracker(t, v.cfg)
	observe(t, first, v.scan(1, 2, 3))
	read(t, first, v.seg(1))

	second, metrics := newTracker(t, v.cfg)
	// While the shadow was down the owner removed 1, 2 and 4.
	if got := observe(t, second, v.scan(3, 5, 6)); got != 2 {
		t.Fatalf("Observe() = %d, want 2: 2 was unread and 4 came and went", got)
	}
	if got := missed(t, metrics); got != 2 {
		t.Fatalf("%s = %v, want 2", telemetry.SegmentsMissed, got)
	}
}

func TestUnreadableTrackerStateStartsFresh(t *testing.T) {
	v := newSpoolView(t)
	first, _ := newTracker(t, v.cfg)
	observe(t, first, v.scan(1, 2))
	entries, err := os.ReadDir(v.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if err := os.WriteFile(filepath.Join(v.cfg.StateDir, entry.Name()), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			corrupted++
		}
	}
	if corrupted == 0 {
		t.Fatal("tracker kept no state under the shadow state directory")
	}

	second, _ := newTracker(t, v.cfg)
	if got := observe(t, second, v.scan(5)); got != 0 {
		t.Fatalf("Observe() = %d, want 0 after starting fresh", got)
	}
}

// An open file stays readable after the owner deletes it, so a read that is
// under way when the next scan misses the file still succeeds.
func TestReadInFlightIsNotCountedMissed(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1, 2))

	missedIt, err := tracker.Track(v.seg(1), func() error {
		if got := observe(t, tracker, v.scan(2)); got != 0 {
			t.Errorf("Observe() = %d while the read was under way, want 0", got)
		}
		return nil
	})
	if missedIt || err != nil {
		t.Fatalf("Track() = %v, %v", missedIt, err)
	}
	if got := observe(t, tracker, v.scan(2)); got != 0 {
		t.Fatalf("Observe() = %d after the read finished, want 0", got)
	}
	if got := missed(t, metrics); got != 0 {
		t.Fatalf("%s = %v, want 0", telemetry.SegmentsMissed, got)
	}
}

func TestFailedReadOfAVanishedSegmentIsCountedLater(t *testing.T) {
	v := newSpoolView(t)
	tracker, _ := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1, 2))

	_, err := tracker.Track(v.seg(1), func() error {
		observe(t, tracker, v.scan(2))
		return errors.New("disk full")
	})
	if err == nil {
		t.Fatal("Track() error = nil")
	}
	if got := observe(t, tracker, v.scan(2)); got != 1 {
		t.Fatalf("Observe() = %d, want 1: the read failed and the source is gone", got)
	}
}

func TestOutcomesBeforeObserveSurviveRestart(t *testing.T) {
	v := newSpoolView(t)
	first, _ := newTracker(t, v.cfg)
	observe(t, first, v.scan(1))
	read(t, first, v.seg(2))
	if !gone(t, first, v.seg(3)) {
		t.Fatal("Track() missed = false")
	}

	second, _ := newTracker(t, v.cfg)
	if got := observe(t, second, v.scan(1, 4)); got != 0 {
		t.Fatalf("Observe() = %d after restart, want 0: 2 was read and 3 already counted", got)
	}
}

func TestReadBeforeObserveAfterTheNumberingRestarted(t *testing.T) {
	v := newSpoolView(t)
	tracker, _ := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(8, 9, 10))
	for _, index := range []int{8, 9, 10} {
		read(t, tracker, v.seg(index))
	}

	read(t, tracker, v.seg(0))
	if got := observe(t, tracker, v.scan(0, 1)); got != 0 {
		t.Fatalf("Observe() = %d, want 0", got)
	}
	read(t, tracker, v.seg(1))
	if got := observe(t, tracker, v.scan(2)); got != 0 {
		t.Fatalf("Observe() = %d, want 0: 0 and 1 were read", got)
	}
}

func TestGapWhileReadInFlightIsNotCounted(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1))

	missedIt, err := tracker.Track(v.seg(2), func() error {
		if got := observe(t, tracker, v.scan(1, 3)); got != 0 {
			t.Errorf("Observe() = %d while 2 was being read, want 0", got)
		}
		return nil
	})
	if missedIt || err != nil {
		t.Fatalf("Track() = %v, %v", missedIt, err)
	}
	if got := observe(t, tracker, v.scan(1, 3)); got != 0 {
		t.Fatalf("Observe() = %d, want 0", got)
	}
	if got := missed(t, metrics); got != 0 {
		t.Fatalf("%s = %v, want 0", telemetry.SegmentsMissed, got)
	}
}

// Guard: the in-flight index skipped as a gap must still be counted when the
// read then finds the source gone.
func TestGoneAfterAScanDuringTheReadIsCountedOnce(t *testing.T) {
	v := newSpoolView(t)
	tracker, metrics := newTracker(t, v.cfg)
	observe(t, tracker, v.scan(1))

	missedIt, err := tracker.Track(v.seg(2), func() error {
		observe(t, tracker, v.scan(1, 3))
		return goneErr(v.seg(2).Path)
	})
	if !missedIt || err != nil {
		t.Fatalf("Track() = %v, %v, want missed", missedIt, err)
	}
	observe(t, tracker, v.scan(1, 3))
	if got := missed(t, metrics); got != 1 {
		t.Fatalf("%s = %v, want 1", telemetry.SegmentsMissed, got)
	}
}

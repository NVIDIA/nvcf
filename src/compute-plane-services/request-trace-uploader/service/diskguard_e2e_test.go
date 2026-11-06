// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/internal/diskguard"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// segmentVolume reports a spool whose used space is the size of its segment
// files, so deleting a segment frees space the way a real volume would.
type segmentVolume struct {
	dir   string
	total atomic.Uint64
	// free, when set, replaces the computed free space.
	free atomic.Int64
}

func (v *segmentVolume) segmentBytes() uint64 {
	entries, _ := os.ReadDir(v.dir)
	var used uint64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl.gz") {
			continue
		}
		if info, err := e.Info(); err == nil {
			used += uint64(info.Size())
		}
	}
	return used
}

func (v *segmentVolume) Usage() (diskguard.Usage, error) {
	total := v.total.Load()
	used := v.segmentBytes()
	free := total - min(used, total)
	if f := v.free.Load(); f > 0 {
		free = uint64(f)
	}
	return diskguard.Usage{Total: total, Free: free}, nil
}

func roomyVolume(dir string) *segmentVolume {
	v := &segmentVolume{dir: dir}
	v.total.Store(1 << 40)
	return v
}

func TestAboveTheHighWatermarkTheOldestClosedSegmentIsEvictedAndCounted(t *testing.T) {
	spool, closed := spoolWith(t,
		[]string{uploadertest.RequestEnd("a", 1)},
		[]string{uploadertest.RequestEnd("b", 2)},
	)
	volume := &segmentVolume{dir: spool.Dir}
	// The three segments fill 90% of the volume; removing the oldest brings
	// it under the 85% watermark.
	volume.total.Store(volume.segmentBytes() * 10 / 9)
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	fake := backendtest.NewFake()
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics, Volume: volume}, nil)

	_ = uploader.Pass(context.Background())
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}

	if !gone(closed[0]) {
		t.Fatal("the oldest closed segment was kept above the high watermark")
	}
	if gone(closed[1]) {
		t.Fatal("a newer closed segment was evicted, want only as much as the watermark needs")
	}
	if got, _ := metrics.Value(telemetry.SegmentsEvicted, nil); got != 1 {
		t.Fatalf("segments_evicted = %v, want the loss counted", got)
	}
	if got := fake.SubmitCalls(); got != 0 {
		t.Fatalf("submits = %d, want preparing paused above the watermark", got)
	}
}

func TestQuarantineOlderThanItsMaxAgeIsRemovedOnAPass(t *testing.T) {
	spool, _ := spoolWith(t)
	entry := filepath.Join(spool.Dir, "request-trace-uploader-quarantine", "old-entry")
	if err := os.MkdirAll(entry, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "segment.jsonl.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{Metrics: metrics, Volume: roomyVolume(spool.Dir), Now: clock}, nil)

	_ = uploader.Pass(context.Background())
	if gone(entry) {
		t.Fatal("a fresh quarantine entry was removed")
	}
	offset.Store(int64(8 * 24 * time.Hour))
	_ = uploader.Pass(context.Background())
	if !gone(entry) {
		t.Fatal("a quarantine entry past its max age was kept")
	}
	if got, _ := metrics.Value(telemetry.QuarantineEvicted, nil); got != 1 {
		t.Fatalf("quarantine_evicted = %v, want the removal counted", got)
	}
}

func TestPreparingPausesWhileFreeSpaceIsLowAndResumesAfter(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	// A small volume, 75% used: under the high watermark, but with less
	// free space than the floor kept for the writer.
	volume := &segmentVolume{dir: spool.Dir}
	volume.total.Store(4 << 20)
	volume.free.Store(1 << 20)
	fake := backendtest.NewFake()
	uploader := newUploader(t, spool, fake, service.Options{Volume: volume}, nil)

	_ = uploader.Pass(context.Background())
	if got := fake.SubmitCalls(); got != 0 {
		t.Fatalf("submits = %d while free space is low, want preparing paused", got)
	}
	if gone(closed[0]) {
		t.Fatal("the segment was deleted while preparing was paused")
	}

	volume.total.Store(1 << 40) // the volume grows
	volume.free.Store(0)        // and free space is computed again
	_ = uploader.Pass(context.Background())
	if got := fake.SubmitCalls(); got != 1 {
		t.Fatalf("submits = %d after space freed up, want the segment prepared and sent", got)
	}
}

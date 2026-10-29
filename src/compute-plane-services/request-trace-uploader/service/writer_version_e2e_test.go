// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// versionedFormat is a backend with its own format writer that reports the
// version of the layout it writes, as a table writer does for its schema. It
// lands jobs like dirBackend, or refuses every submit.
type versionedFormat struct {
	dirBackend
	refuse  bool
	version string
	opened  *atomic.Int32
}

func (v versionedFormat) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	if v.refuse {
		return refusingBackend{}.Submit(ctx, request)
	}
	return v.dirBackend.Submit(ctx, request)
}

func (versionedFormat) Format() backend.Format { return backend.FormatGzipJSONL }

func (v versionedFormat) NewWriter(target format.Target) (format.Writer, error) {
	v.opened.Add(1)
	return format.GzipJSONL{}.NewWriter(target)
}

func (v versionedFormat) FormatVersion() string { return v.version }

func TestAPreparedFileIsPreparedAgainOnlyWhenTheWriterVersionChanges(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
		wantPrepared  bool
	}{
		{"same writer version", "1", "1", false},
		{"new writer version", "1", "2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			landing := t.TempDir()
			spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
			var before, after atomic.Int32

			first := newUploader(t, spool, versionedFormat{dirBackend: dirBackend{root: landing}, refuse: true, version: tc.before, opened: &before}, service.Options{}, nil)
			_ = first.Pass(context.Background())
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if batches := journalBatches(t, spool); before.Load() != 1 || len(batches) != 1 || batches[0].State != journal.StateRetryWait {
				t.Fatalf("writers opened = %d, batches = %+v; want the segment prepared once and its submit refused", before.Load(), batches)
			}

			restarted := newUploader(t, spool, versionedFormat{dirBackend: dirBackend{root: landing}, version: tc.after, opened: &after}, service.Options{}, nil)
			passUntil(t, restarted, func() bool { return gone(closed[0]) })
			if prepared := after.Load() > 0; prepared != tc.wantPrepared {
				t.Fatalf("prepared again after the restart = %t (writers opened %d), want %t", prepared, after.Load(), tc.wantPrepared)
			}
		})
	}
}

// A file prepared under an older writer is normally discarded and prepared
// again from its source. When the source is gone, the file is the only copy:
// it is kept in quarantine, neither discarded nor sent under settings it was
// not prepared for.
func TestAStaleFileWhoseSourceIsGoneIsKeptInQuarantine(t *testing.T) {
	landing := t.TempDir()
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	var opened atomic.Int32
	first := newUploader(t, spool, versionedFormat{dirBackend: dirBackend{root: landing}, refuse: true, version: "1", opened: &opened}, service.Options{}, nil)
	_ = first.Pass(context.Background())
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(closed[0]); err != nil {
		t.Fatal(err)
	}

	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	restarted := newUploader(t, spool, versionedFormat{dirBackend: dirBackend{root: landing}, version: "2", opened: &opened}, service.Options{Metrics: metrics}, nil)
	for range 3 {
		_ = restarted.Pass(context.Background())
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	if got := quarantinedBy(metrics, "source_missing"); got != 1 {
		t.Fatalf("%s{reason=source_missing} = %v, want 1", telemetry.SegmentsQuarantined, got)
	}
	if state := journalStates(t, spool)[filepath.Base(closed[0])]; state != journal.StateQuarantined {
		t.Fatalf("state = %q, want quarantined", state)
	}
	kept, err := filepath.Glob(filepath.Join(spool.Dir, "request-trace-uploader-quarantine", "*", "*.jsonl.gz"))
	if err != nil || len(kept) != 1 {
		t.Fatalf("quarantined transports = %v, %v; want the only copy kept", kept, err)
	}
	if jobs, _ := filepath.Glob(filepath.Join(landing, "*", "landed")); len(jobs) != 0 {
		t.Fatalf("landed jobs = %d, want the stale file not sent", len(jobs))
	}
}

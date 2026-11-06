// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

func batchingFake() *backendtest.Fake {
	fake := backendtest.NewFake()
	fake.SetCapabilities(backend.Capabilities{
		AcceptedFormats: []backend.Format{backend.FormatGzipJSONL},
		Exports:         true,
		MaxBatchFiles:   16,
	})
	return fake
}

func journalBatches(t *testing.T, spool *uploadertest.Spool) []journal.Batch {
	t.Helper()
	store, err := journalfs.Open(context.Background(), journalfs.Config{
		SpoolDir: spool.Dir,
		StateDir: filepath.Join(spool.Dir, "request-trace-uploader-state"),
		Holder:   "inspector",
		LockWait: time.Second,
	}, journalfs.Options{})
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer store.Close()
	return store.Batches()
}

// readOnly makes dir unwritable until restore is called or the test ends.
func readOnly(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	restore = func() { once.Do(func() { _ = os.Chmod(dir, 0o750) }) }
	t.Cleanup(restore)
	return restore
}

func passUntil(t *testing.T, uploader *service.Uploader, done func() bool) {
	t.Helper()
	waitFor(t, "the pipeline to finish", func() bool {
		_ = uploader.Pass(context.Background())
		return done()
	})
}

func TestARejectArtifactThatCannotBeKeptHoldsTheSegment(t *testing.T) {
	spool, closed := spoolWith(t, []string{"not json", uploadertest.RequestEnd("a", 1)})
	data, err := os.ReadFile(closed[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	id := journal.SegmentID(config.DefaultSegmentPrefix, 0, int64(len(data)), hex.EncodeToString(sum[:]))
	// A directory where the artifact must go makes keeping it fail.
	blocker := filepath.Join(spool.Dir, "request-trace-uploader-quarantine", "rejects", id+".jsonl.gz")
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	fake := backendtest.NewFake()
	uploader := newUploader(t, spool, fake, service.Options{}, nil)

	_ = uploader.Pass(context.Background())
	if fake.SubmitCalls() != 0 || gone(closed[0]) {
		t.Fatalf("submit calls = %d, source gone = %v; want nothing sent while the rejected line has no durable copy", fake.SubmitCalls(), gone(closed[0]))
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	passUntil(t, uploader, func() bool { return gone(closed[0]) })
	if _, err := os.Stat(blocker); err != nil {
		t.Fatalf("reject artifact: %v, want it kept", err)
	}
}

func TestABatchWhoseMembersCouldNotBeRecordedIsNotSentTwice(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)}, []string{uploadertest.RequestEnd("b", 2)})
	segments := filepath.Join(spool.Dir, "request-trace-uploader-state", "segments")
	var restore func()
	prepared := 0
	fake := batchingFake()
	uploader := newUploader(t, spool, fake, service.Options{Point: func(name string) {
		if name == service.PointPrepared {
			if prepared++; prepared == 2 {
				restore = readOnly(t, segments)
			}
		}
	}}, nil)

	_ = uploader.Pass(context.Background())
	restore()
	passUntil(t, uploader, func() bool { return gone(closed[0]) && gone(closed[1]) })
	for range 2 {
		_ = uploader.Pass(context.Background())
	}
	if fake.Duplicates() != 0 {
		t.Fatalf("duplicates = %d, want each segment landed once", fake.Duplicates())
	}
}

func TestARetainedSubmitWaitsForAnOperator(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Outcome: backendtest.Refuse, Err: &backend.Fault{Class: backend.ClassUncertain, Scope: backend.ScopeUnknown, Reason: backend.ReasonTimeout}})
	faults := fastFaults()
	faults.AmbiguousWithoutID = retry.AmbiguousRetain
	uploader := newUploader(t, spool, fake, service.Options{Faults: faults}, nil)
	for range 3 {
		_ = uploader.Pass(context.Background())
	}
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.SubmitCalls() != 1 || gone(closed[0]) {
		t.Fatalf("submit calls = %d, want the retained batch left alone", fake.SubmitCalls())
	}
	for _, b := range journalBatches(t, spool) {
		if b.NextRetryAt.Before(time.Now().Add(365 * 24 * time.Hour)) {
			t.Fatalf("retained batch retries at %v, want it never retried automatically", b.NextRetryAt)
		}
	}
}

func TestAJobIDThatCouldNotBeSavedIsSavedLaterWithoutAnotherSubmit(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	batches := filepath.Join(spool.Dir, "request-trace-uploader-state", "batches")
	var restore func()
	fake := backendtest.NewFake()
	uploader := newUploader(t, spool, fake, service.Options{Point: func(name string) {
		if name == service.PointSubmitted && restore == nil {
			restore = readOnly(t, batches)
		}
	}}, nil)

	_ = uploader.Pass(context.Background())
	restore()
	passUntil(t, uploader, func() bool { return gone(closed[0]) })
	if fake.SubmitCalls() != 1 {
		t.Fatalf("submit calls = %d, want 1", fake.SubmitCalls())
	}
}

func TestPollingIsNotHeldUpByASlowSubmit(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(
		backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusPending}, {Status: backend.StatusSucceeded}}},
		backendtest.SubmitStep{Delay: 5 * time.Second},
	)
	uploader := newUploader(t, spool, fake, service.Options{}, nil)
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("next-active", 3)).Bytes())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := make(chan struct{})
	go func() {
		defer close(slow)
		_ = uploader.Pass(ctx)
	}()
	waitFor(t, "the slow submit to start", func() bool { return fake.SubmitCalls() == 2 })

	started := time.Now()
	if err := uploader.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Poll took %v behind a slow submit, want it independent", elapsed)
	}
	if !gone(closed[0]) {
		t.Fatal("the first job's success was not applied")
	}
	cancel()
	<-slow
}

func TestASegmentWhoseJobsKeepFailingIsQuarantined(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	failing := backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{
		Status: backend.StatusFailed,
		Err:    &backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeSegment, Reason: backend.ReasonRejected},
	}}}
	for range 20 {
		fake.Script(failing)
	}
	uploader := newUploader(t, spool, fake, service.Options{}, nil)
	for range 12 {
		_ = uploader.Pass(context.Background())
		time.Sleep(20 * time.Millisecond)
	}
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	if state := journalStates(t, spool)[filepath.Base(closed[0])]; state != journal.StateQuarantined {
		t.Fatalf("state = %q after %d failed jobs, want quarantined", state, fake.SubmitCalls())
	}
}

func TestErrorLogsCarryNoSpoolPaths(t *testing.T) {
	logs := captureLogs(t)
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	if err := os.Chmod(closed[0], 0); err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{}, nil)
	_ = uploader.Pass(context.Background())
	if !strings.Contains(logs.String(), "permission denied") {
		t.Fatalf("logs = %s, want the read failure logged", logs)
	}
	if strings.Contains(logs.String(), spool.Dir) {
		t.Fatalf("logs = %s, want no spool path", logs)
	}
}

func TestASegmentIsRetriedUntilItReachesTheFailureThreshold(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	refused := backendtest.SubmitStep{Outcome: backendtest.Refuse, Err: &backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeSegment, Reason: backend.ReasonRejected}}
	faults := fastFaults()
	faults.SegmentScopeFailures = 5
	for range faults.SegmentScopeFailures - 1 {
		fake.Script(refused)
	}
	uploader := newUploader(t, spool, fake, service.Options{Faults: faults}, nil)
	passUntil(t, uploader, func() bool { return gone(closed[0]) || fake.SubmitCalls() > faults.SegmentScopeFailures })
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	if state := journalStates(t, spool)[filepath.Base(closed[0])]; state != journal.StateDone {
		t.Fatalf("state = %q after %d refused submits under a threshold of %d, want done", state, faults.SegmentScopeFailures-1, faults.SegmentScopeFailures)
	}
}

func TestIntakeStopsAsSoonAsTheQuarantineBreakerTrips(t *testing.T) {
	bad := make([][]string, 5)
	for i := range bad {
		bad[i] = []string{"not json " + strings.Repeat("x", i)}
	}
	spool, closed := spoolWith(t, bad...)
	faults := fastFaults()
	faults.QuarantineTrip = 2
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{Faults: faults}, nil)
	_ = uploader.Pass(context.Background())
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	quarantined := 0
	states := journalStates(t, spool)
	for _, path := range closed {
		if states[filepath.Base(path)] == journal.StateQuarantined {
			quarantined++
		}
	}
	if quarantined != faults.QuarantineTrip {
		t.Fatalf("quarantined %d segments with a breaker that trips at %d, want intake to stop at the trip", quarantined, faults.QuarantineTrip)
	}
}

// TestTheQuarantineBreakerResumesAfterTheConfiguredTime runs the breaker
// from the configured settings, with no fault handling override: intake
// stops at the trip, reads new segments again once the resume time has
// passed, and trips again after the same number of quarantines.
func TestTheQuarantineBreakerResumesAfterTheConfiguredTime(t *testing.T) {
	bad := make([][]string, 6)
	for i := range bad {
		bad[i] = []string{"not json " + strings.Repeat("x", i)}
	}
	spool, closed := spoolWith(t, bad...)
	var offset atomic.Int64
	start := time.Now()
	clock := func() time.Time { return start.Add(time.Duration(offset.Load())) }
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{
		config.EnvQuarantineTrip:   "2",
		config.EnvQuarantineResume: "60",
	}))
	uploader, err := service.NewUploader(cfg, backendtest.NewFake(), service.Options{Metrics: metrics, Now: clock})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	t.Cleanup(func() { _ = uploader.Close() })
	if got, ok := metrics.Value(telemetry.QuarantineResumed, nil); !ok || got != 0 {
		t.Fatalf("quarantine_resumed at start = %v, %v; want 0, true", got, ok)
	}

	counter := func(name string) float64 {
		got, _ := metrics.Value(name, nil)
		return got
	}
	quarantined := func() float64 {
		var n float64
		for _, reason := range telemetry.QuarantineReasons {
			got, _ := metrics.Value(telemetry.SegmentsQuarantined, telemetry.Labels{"reason": reason})
			n += got
		}
		return n
	}
	steps := []struct {
		at                         time.Duration
		quarantined, trips, resume float64
	}{
		{at: 0, quarantined: 2, trips: 1, resume: 0},
		{at: 59 * time.Second, quarantined: 2, trips: 1, resume: 0},
		{at: 60 * time.Second, quarantined: 4, trips: 2, resume: 1},
		{at: 119 * time.Second, quarantined: 4, trips: 2, resume: 1},
		{at: 120 * time.Second, quarantined: 6, trips: 3, resume: 2},
	}
	for _, step := range steps {
		offset.Store(int64(step.at))
		_ = uploader.Pass(context.Background())
		if got := quarantined(); got != step.quarantined {
			t.Fatalf("at %v: quarantined %v segments, want %v", step.at, got, step.quarantined)
		}
		if got := counter(telemetry.QuarantineTripped); got != step.trips {
			t.Fatalf("at %v: quarantine_tripped = %v, want %v", step.at, got, step.trips)
		}
		if got := counter(telemetry.QuarantineResumed); got != step.resume {
			t.Fatalf("at %v: quarantine_resumed = %v, want %v", step.at, got, step.resume)
		}
	}
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	states := journalStates(t, spool)
	for _, path := range closed {
		if states[filepath.Base(path)] != journal.StateQuarantined {
			t.Fatalf("segment %s is %q, want every bad segment quarantined after two resumes", filepath.Base(path), states[filepath.Base(path)])
		}
	}
}

func TestAConfirmationWhoseMembersWereNotRecordedIsFinishedWithoutARestart(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	segments := filepath.Join(spool.Dir, "request-trace-uploader-state", "segments")
	var restore func()
	fake := backendtest.NewFake()
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics, Point: func(name string) {
		if name == service.PointReceiptSaved && restore == nil {
			restore = readOnly(t, segments)
		}
	}}, nil)

	_ = uploader.Pass(context.Background())
	restore()
	if gone(closed[0]) {
		t.Fatal("source deleted while its confirmation was not recorded")
	}
	passUntil(t, uploader, func() bool { return gone(closed[0]) })
	if fake.SubmitCalls() != 1 {
		t.Fatalf("submit calls = %d, want 1", fake.SubmitCalls())
	}
	if got, _ := metrics.Value(telemetry.Uploads, telemetry.Labels{"status": "success"}); got != 1 {
		t.Errorf("uploads{success} = %v, want 1", got)
	}
}

func TestOneSegmentWhoseJobsKeepFailingIsQuarantinedWithoutHoldingTheBackend(t *testing.T) {
	logs := captureLogs(t)
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)}, []string{uploadertest.RequestEnd("b", 2)})
	fake := backendtest.NewFake()
	// A failed job with no reason is an unknown-scope fault.
	failing := backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusFailed}}}
	fake.Script(failing, backendtest.SubmitStep{})
	for range 20 {
		fake.Script(failing)
	}
	uploader := newUploader(t, spool, fake, service.Options{}, nil)
	passUntil(t, uploader, func() bool { return gone(closed[0]) && gone(closed[1]) })
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	states := journalStates(t, spool)
	if state := states[filepath.Base(closed[0])]; state != journal.StateQuarantined {
		t.Fatalf("failing segment state = %q after %d submits, want quarantined", state, fake.SubmitCalls())
	}
	if state := states[filepath.Base(closed[1])]; state != journal.StateDone {
		t.Fatalf("healthy segment state = %q, want done", state)
	}
	if strings.Contains(logs.String(), `"action":"hold"`) {
		t.Fatalf("one failing segment held the whole backend:\n%s", logs)
	}
}

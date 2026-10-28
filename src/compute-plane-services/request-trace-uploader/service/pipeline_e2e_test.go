// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// fastFaults keeps the decided fault rules but shortens every wait, so a
// test with many creates does not sit in the create budget.
func fastFaults() *retry.Config {
	cfg := retry.DefaultConfig()
	cfg.Transient = retry.Backoff{Base: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	cfg.Auth = retry.Backoff{Base: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	cfg.ResumeJitter = 0
	cfg.CreatesPerMinute = 60000
	cfg.CreateBurst = 1000
	return &cfg
}

// spoolWith writes one closed segment per entry of segments, then an active
// segment, and returns the spool and the closed paths.
func spoolWith(t *testing.T, segments ...[]string) (*uploadertest.Spool, []string) {
	t.Helper()
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	var closed []string
	for _, lines := range segments {
		closed = append(closed, spool.Roll(uploadertest.NewSegment().Member(lines...).Bytes()))
	}
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("active", 1)).Bytes())
	return spool, closed
}

func newUploader(t *testing.T, spool *uploadertest.Spool, client backend.Client, opts service.Options, env map[string]string) *service.Uploader {
	t.Helper()
	if opts.Faults == nil {
		opts.Faults = fastFaults()
	}
	uploader, err := service.NewUploader(loadEnv(t, spoolEnv(t, spool.Dir, env)), client, opts)
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	t.Cleanup(func() { _ = uploader.Close() })
	return uploader
}

// journalStates opens the journal after the uploader released it and lists
// every segment state by file name.
func journalStates(t *testing.T, spool *uploadertest.Spool) map[string]journal.State {
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
	states := map[string]journal.State{}
	for _, seg := range store.Segments() {
		states[seg.Name] = seg.State
	}
	return states
}

func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func TestPipelineBatchesExportsAndCleansUpThroughTheJournal(t *testing.T) {
	spool, closed := spoolWith(t,
		[]string{uploadertest.RequestEnd("a", 1), uploadertest.RequestEnd("b", 2)},
		[]string{uploadertest.RequestEnd("c", 3)},
		[]string{uploadertest.RequestPayload("d", 4, nil)},
	)
	fake := backendtest.NewFake()
	fake.SetCapabilities(backend.Capabilities{
		OutOfOrderTolerant: true,
		AcceptedFormats:    []backend.Format{backend.FormatGzipJSONL},
		Exports:            true,
		MaxBatchFiles:      16,
	})
	exporter := newCapture(t, fake)
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, exporter, service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	jobs := fake.Jobs()
	if len(jobs) != 1 || len(jobs[0].Files) != 3 {
		t.Fatalf("jobs = %+v, want one job carrying the three segments", jobs)
	}
	if got := len(exporter.exported()); got != 4 {
		t.Fatalf("exported %d records, want 4", got)
	}
	for _, path := range closed {
		if !gone(path) {
			t.Errorf("source %s kept after a confirmed export", filepath.Base(path))
		}
	}
	if got, _ := metrics.Value(telemetry.Uploads, telemetry.Labels{"status": "success"}); got != 1 {
		t.Errorf("uploads{success} = %v, want 1", got)
	}
	if got, _ := metrics.Value(telemetry.Deletions, telemetry.Labels{"status": "success"}); got != 3 {
		t.Errorf("deletions{success} = %v, want 3", got)
	}
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range closed {
		if state := journalStates(t, spool)[filepath.Base(path)]; state != journal.StateDone {
			t.Errorf("journal state of %s = %q, want done", filepath.Base(path), state)
		}
	}
}

func TestAJobIsPolledUntilSuccessAndNeverSubmittedAgain(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{
		{Status: backend.StatusPending}, {Status: backend.StatusPending}, {Status: backend.StatusSuccess},
	}})
	first := newUploader(t, spool, fake, service.Options{}, nil)

	if err := first.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gone(closed[0]) {
		t.Fatal("source deleted while its job is still pending")
	}
	// A restart reads the saved job id and polls it; it never submits the
	// segment again.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newUploader(t, spool, fake, service.Options{}, nil)
	if err := second.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gone(closed[0]) {
		t.Fatal("source kept after the job reported success")
	}
	if fake.SubmitCalls() != 1 {
		t.Fatalf("submit calls = %d, want 1", fake.SubmitCalls())
	}
}

func TestUnknownJobStatusIsCountedAndPolledAgain(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{
		{Status: "QUEUED_SOMEWHERE"}, {Status: backend.StatusSuccess},
	}})
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics}, nil)
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := metrics.Value(telemetry.UnknownStatus, nil); got != 1 {
		t.Fatalf("unknown_status = %v, want 1", got)
	}
	if gone(closed[0]) {
		t.Fatal("source deleted on an unknown status")
	}
	if err := uploader.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gone(closed[0]) {
		t.Fatal("source kept after the next poll reported success")
	}
}

func TestANonExportingBackendNeverDeletesASource(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.SetCapabilities(backend.Capabilities{AcceptedFormats: []backend.Format{backend.FormatGzipJSONL}})
	uploader := newUploader(t, spool, fake, service.Options{}, nil)
	for range 2 {
		if err := uploader.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if gone(closed[0]) {
		t.Fatal("a backend that does not export deleted the only copy")
	}
	if fake.SubmitCalls() != 1 {
		t.Fatalf("submit calls = %d, want 1: a read segment is not read again", fake.SubmitCalls())
	}
}

type metricsBackend struct {
	*backendtest.Fake
	metrics telemetry.Metrics
}

func (m *metricsBackend) UseMetrics(metrics telemetry.Metrics) { m.metrics = metrics }

func TestABackendThatRecordsMetricsGetsTheSharedRecorder(t *testing.T) {
	spool, _ := spoolWith(t)
	recorder, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	client := &metricsBackend{Fake: backendtest.NewFake()}
	newUploader(t, spool, client, service.Options{Metrics: recorder}, nil)
	if client.metrics == nil {
		t.Fatal("the backend was not given a recorder")
	}
	client.metrics.Add(telemetry.UnknownStatus, 1, nil)
	if got, _ := recorder.Value(telemetry.UnknownStatus, nil); got != 1 {
		t.Fatalf("unknown_status = %v, want the backend's observation in the shared recorder", got)
	}
}

func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener, "http://" + listener.Addr().String()
}

var probe = &http.Client{Timeout: time.Second}

func statusOf(url string) int {
	response, err := probe.Get(url) //nolint:noctx // short test probe
	if err != nil {
		return 0
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthAnswersBeforeReadyWhileAnotherUploaderHoldsTheSpool(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	holder, err := journalfs.Open(context.Background(), journalfs.Config{
		SpoolDir: spool.Dir,
		StateDir: filepath.Join(spool.Dir, "request-trace-uploader-state"),
		Holder:   "old-uploader",
		LockWait: time.Second,
	}, journalfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	listener, base := listen(t)
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{Listener: listener}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()

	waitFor(t, "liveness", func() bool { return statusOf(base+"/livez") == http.StatusOK })
	if got := statusOf(base + "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz while the lock is held = %d, want 503", got)
	}
	if !strings.Contains(closed[0], spool.Dir) || gone(closed[0]) {
		t.Fatal("a segment was touched before the lock was taken")
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "readiness", func() bool { return statusOf(base+"/readyz") == http.StatusOK })
	waitFor(t, "the first pass", func() bool { return gone(closed[0]) })
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestAnUnreadableJournalKeepsTheProcessAliveButNotReady(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	segments := filepath.Join(spool.Dir, "request-trace-uploader-state", "segments")
	if err := os.MkdirAll(segments, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segments, "no-segment-name.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, base := listen(t)
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{Listener: listener}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()

	waitFor(t, "liveness", func() bool { return statusOf(base+"/livez") == http.StatusOK })
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run() returned %v, want the process kept alive", err)
	default:
	}
	if got := statusOf(base + "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503 while the journal is unreadable", got)
	}
	if gone(closed[0]) {
		t.Fatal("a segment was exported while the journal is unreadable")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSIGTERMStopsWithinTheDrainTimeoutAndKeepsTheBatchForTheNextStart(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	// The create hangs past the drain timeout, as a stalled destination
	// would.
	fake.Script(backendtest.SubmitStep{Delay: time.Hour})
	listener, base := listen(t)
	uploader := newUploader(t, spool, fake, service.Options{Listener: listener}, map[string]string{config.EnvDrainTimeout: "1"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()
	waitFor(t, "readiness", func() bool { return statusOf(base+"/readyz") == http.StatusOK })
	waitFor(t, "the create to start", func() bool { return fake.SubmitCalls() == 1 })

	cancel()
	stopped := time.Now()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within the 1 s drain timeout")
	}
	if elapsed := time.Since(stopped); elapsed > 3*time.Second {
		t.Fatalf("drain took %v, want about 1 s", elapsed)
	}
	if gone(closed[0]) {
		t.Fatal("source deleted although the create never finished")
	}
	// The next start finds the unfinished batch and submits it again once
	// its short retry delay has passed.
	next := newUploader(t, spool, fake, service.Options{}, nil)
	waitFor(t, "the next start to upload the segment", func() bool {
		if err := next.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		return gone(closed[0])
	})
}

func TestRunPollsOnTheStatusIntervalNotOnEveryScan(t *testing.T) {
	spool, _ := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusPending}}})
	listener, _ := listen(t)
	uploader := newUploader(t, spool, fake, service.Options{Listener: listener}, map[string]string{
		config.EnvScanInterval:   "1",
		config.EnvStatusInterval: "3600",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()

	waitFor(t, "the first submit", func() bool { return len(fake.Jobs()) == 1 })
	// Three more scan passes run in this time.
	time.Sleep(3500 * time.Millisecond)
	statusCalls := fake.Jobs()[0].StatusCalls
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if statusCalls > 1 {
		t.Fatalf("status calls = %d over several 1s scans with a 1h status interval, want at most 1", statusCalls)
	}
}

// blockingSubmits holds every submit until release is closed.
type blockingSubmits struct {
	*backendtest.Fake
	release chan struct{}
}

func (b blockingSubmits) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return b.Fake.Submit(ctx, request)
}

func TestAJobFromBeforeARestartIsConfirmedWhileTheFirstScanIsStillSubmitting(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusPending}, {Status: backend.StatusSuccess}}})
	first := newUploader(t, spool, fake, service.Options{}, nil)
	if err := first.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// A new segment closes, so the first scan after the restart submits.
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("b", 2)).Bytes())

	blocked := blockingSubmits{Fake: fake, release: make(chan struct{})}
	listener, _ := listen(t)
	uploader := newUploader(t, spool, blocked, service.Options{Listener: listener}, map[string]string{
		config.EnvScanInterval:   "1",
		config.EnvStatusInterval: "1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()
	defer func() {
		close(blocked.release)
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	}()

	waitFor(t, "the earlier job to be confirmed and its source deleted", func() bool { return gone(closed[0]) })
}

func TestAReceiptFromADryRunNeverDeletesTheSourceAfterARestart(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	dryRun := fake.Capabilities()
	dryRun.Exports = false
	fake.SetCapabilities(dryRun)
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusPending}, {Status: backend.StatusSuccess}}})
	first := newUploader(t, spool, fake, service.Options{}, nil)
	if err := first.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	exporting := fake.Capabilities()
	exporting.Exports = true
	fake.SetCapabilities(exporting)
	second := newUploader(t, spool, fake, service.Options{}, nil)
	for range 2 {
		if err := second.Pass(context.Background()); err != nil {
			t.Fatalf("Pass() after restart error = %v", err)
		}
	}
	if fake.Jobs()[0].StatusCalls < 2 {
		t.Fatal("the dry-run job was not confirmed after the restart")
	}
	if gone(closed[0]) {
		t.Fatal("source deleted on the strength of a dry-run receipt, want it kept")
	}
}

func TestStartupDoesNotWarnWhenDefaultBatchLimitsExceedTheBackend(t *testing.T) {
	for name, fake := range map[string]*backendtest.Fake{"one file per job": backendtest.NewFake(), "batching": batchingFake()} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			spool, _ := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
			uploader := newUploader(t, spool, fake, service.Options{}, nil)
			if err := uploader.Pass(context.Background()); err != nil {
				t.Fatalf("Pass() error = %v", err)
			}
			if strings.Contains(logs.String(), "batch setting lowered") {
				t.Fatalf("logs = %s, want no warning for a default the operator did not set", logs)
			}
		})
	}
}

func TestDrainTakesASegmentThatClosedAfterTheLastScan(t *testing.T) {
	cases := []struct {
		name string
		// slow holds the second create past the drain timeout.
		slow bool
	}{
		{name: "exported within the drain"},
		{name: "kept and journaled when the drain runs out", slow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spool, _ := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
			lastActive := filepath.Join(spool.Dir, config.DefaultSegmentPrefix+".000001.jsonl.gz")
			fake := backendtest.NewFake()
			if tc.slow {
				fake.Script(backendtest.SubmitStep{}, backendtest.SubmitStep{Delay: time.Minute})
			}
			listener, _ := listen(t)
			uploader := newUploader(t, spool, fake, service.Options{Listener: listener}, map[string]string{
				config.EnvScanInterval:   "3600",
				config.EnvStatusInterval: "3600",
				config.EnvDrainTimeout:   "1",
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- uploader.Run(ctx) }()
			waitFor(t, "the first submit", func() bool { return len(fake.Jobs()) == 1 })

			// The writer rolls: the segment the last scan saw as active
			// is closed now, and SIGTERM comes before the next scan.
			spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("next-active", 3)).Bytes())
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() error = %v", err)
			}

			if tc.slow {
				if gone(lastActive) {
					t.Fatal("source deleted though its export did not finish, want it kept")
				}
				if _, ok := journalStates(t, spool)[filepath.Base(lastActive)]; !ok {
					t.Fatal("the segment that closed after the last scan is not in the journal")
				}
				return
			}
			if !gone(lastActive) {
				t.Fatalf("the segment that closed after the last scan was left on the spool; jobs = %d", len(fake.Jobs()))
			}
		})
	}
}

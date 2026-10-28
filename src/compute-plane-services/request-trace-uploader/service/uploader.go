// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/diskguard"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

// DefaultHeaderCheckRecords is how many payload records the uploader reads,
// with suppression on, before it warns that none carried a rule's header.
const DefaultHeaderCheckRecords = 100

// lockMargin is added to the drain timeout to bound the wait for the spool
// lock: an earlier uploader gets its whole drain to let go.
const lockMargin = 10 * time.Second

// Named points in the pipeline. Options.Point is called at each one, so a
// test can stop the process there and check that a restart loses and
// double-deletes nothing.
const (
	// PointPrepared follows the record of a prepared segment.
	PointPrepared = "prepared"
	// PointBatchRecorded follows the record of a batch, before the backend
	// is called.
	PointBatchRecorded = "batch-recorded"
	// PointSubmitted follows a create that returned, before its job id is
	// saved.
	PointSubmitted = "submitted"
	// PointReceiptSaved follows the save of a job id.
	PointReceiptSaved = "receipt-saved"
	// PointConfirmed follows the record of a confirmed batch, before
	// cleanup.
	PointConfirmed = "confirmed"
	// PointCleaning follows the record that cleanup started, before any
	// file is deleted.
	PointCleaning = "cleaning"
	// PointSourceDeleted follows the deletion of a source, before Done is
	// recorded.
	PointSourceDeleted = "source-deleted"
)

// Options tunes an Uploader beyond its configuration.
type Options struct {
	// Metrics receives the pipeline's observations. Nil records nothing.
	Metrics telemetry.Metrics
	// HeaderCheckRecords overrides DefaultHeaderCheckRecords.
	HeaderCheckRecords int
	// Faults overrides the fault handling defaults. Nil uses
	// retry.DefaultConfig. Tests shorten its waits.
	Faults *retry.Config
	// Listener serves health instead of a listener on the configured
	// address. Tests pass one bound to a free port.
	Listener net.Listener
	// Holder names this uploader in the spool lock. Empty uses the host
	// name.
	Holder string
	// Point, when set, is called at each named point. Only tests set it.
	Point func(name string)
}

// Uploader runs the export pipeline against a durable journal. Each pass
// reads every new closed segment, applies suppression and then redaction,
// writes the records in the backend's transport format, batches prepared
// segments by destination, submits each batch once its record is saved,
// polls every open job, and deletes a source only after its job reported
// success. Every step is recorded first, so a restart resumes from the
// journal: a saved job id is polled, never submitted again.
type Uploader struct {
	cfg      config.Config
	client   backend.Client
	caps     backend.Capabilities
	name     string
	preparer *transform.Preparer
	factory  format.Factory
	// prepPolicy fingerprints how records are handled; see recordPolicy.
	prepPolicy string
	faults     retry.Config
	policy     *retry.Policy
	metrics    telemetry.Metrics
	rules      suppressionRules
	watch      *headerWatch
	health     *health.Handler
	quarantine *diskguard.Quarantine
	listener   net.Listener
	holder     string
	point      func(string)

	startOnce sync.Once
	startErr  error
	store     *journalfs.Store
	cleaner   *cleanup.Cleaner
	batcher   *batch.Batcher

	// Each stage has its own lock, so polling never waits behind a slow
	// submit or a long intake. Close takes them all, in this order.
	intakeMu  sync.Mutex
	submitMu  sync.Mutex
	pollMu    sync.Mutex
	cleanupMu sync.Mutex
	// fingerprints is used by intake only.
	fingerprints map[fileIdentity]string
	// stateMu guards the maps below, which several stages share.
	stateMu    sync.Mutex
	solo       map[string]bool
	lastPolled map[string]time.Time
	// unsaved holds job ids the journal could not record yet, by batch.
	unsaved map[string]string
	// failures counts failed jobs per segment across replacement batches.
	failures map[string]int
	draining atomic.Bool
}

// NewUploader builds the pipeline around an already-built backend. It
// checks everything that does not touch the spool, so a wiring mistake
// fails at startup.
func NewUploader(cfg config.Config, client backend.Client, opts Options) (*Uploader, error) {
	if client == nil {
		return nil, errors.New("request trace uploader needs a backend")
	}
	if cfg.Backend == "" {
		return nil, errors.New("request trace uploader needs a backend name")
	}
	rules, err := newSuppressionRules(cfg.Suppression)
	if err != nil {
		return nil, err
	}
	prepareConfig := transform.DefaultConfig()
	prepareConfig.Suppression = cfg.Suppression
	prepareConfig.Redact = !cfg.SkipRedaction
	preparer, err := transform.New(prepareConfig)
	if err != nil {
		return nil, err
	}
	factory, err := format.For(client)
	if err != nil {
		return nil, err
	}
	prepPolicy, err := recordPolicy(prepareConfig, factory.Format())
	if err != nil {
		return nil, err
	}
	faults := retry.DefaultConfig()
	if opts.Faults != nil {
		faults = *opts.Faults
	}
	policy, err := retry.NewPolicy(faults, nil)
	if err != nil {
		return nil, err
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = discardMetrics{}
	}
	metrics = telemetry.Safe(metrics)
	telemetry.Preset(metrics)
	rules.preset(metrics)
	metrics.Add(telemetry.RecordsDropped, 0, unsupportedVersionLabels())
	if receiver, ok := client.(telemetry.Receiver); ok {
		receiver.UseMetrics(metrics)
	}
	threshold := opts.HeaderCheckRecords
	if threshold <= 0 {
		threshold = DefaultHeaderCheckRecords
	}
	holder := opts.Holder
	if holder == "" {
		if holder, err = os.Hostname(); err != nil || holder == "" {
			holder = "request-trace-uploader"
		}
	}
	point := opts.Point
	if point == nil {
		point = func(string) {}
	}
	return &Uploader{
		cfg:          cfg,
		client:       client,
		caps:         client.Capabilities(),
		name:         string(cfg.Backend),
		preparer:     preparer,
		factory:      factory,
		prepPolicy:   prepPolicy,
		faults:       faults,
		policy:       policy,
		metrics:      metrics,
		rules:        rules,
		watch:        newHeaderWatch(preparer.SuppressionActive(), threshold, rules.names()),
		health:       health.New(),
		quarantine:   diskguard.NewQuarantine(cfg.QuarantineDir),
		listener:     opts.Listener,
		holder:       holder,
		point:        point,
		fingerprints: map[fileIdentity]string{},
		solo:         map[string]bool{},
		lastPolled:   map[string]time.Time{},
		unsaved:      map[string]string{},
		failures:     map[string]int{},
	}, nil
}

// Handler returns the health endpoints.
func (u *Uploader) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", u.health.Live)
	mux.HandleFunc("GET /readyz", u.health.Ready)
	return mux
}

// errNotReady stops startup without stopping the process: the uploader stays
// live and not ready until it is told to stop.
var errNotReady = errors.New("request trace uploader cannot start safely")

// Run serves health at once, runs the startup steps, reports ready, and then
// runs scan passes on the scan interval and poll rounds on the status
// interval until ctx ends. Then it drains: it lets work in progress
// finish, scans the spool one last time, submits what is prepared and polls
// once more, all within the drain timeout, and releases
// the spool. A backend or spool fault never stops the process; a journal it
// cannot read keeps it live and not ready.
func (u *Uploader) Run(ctx context.Context) error {
	listener := u.listener
	if listener == nil {
		var err error
		if listener, err = net.Listen("tcp", u.cfg.HealthAddr); err != nil {
			return fmt.Errorf("listen for uploader health checks: %w", err)
		}
	}
	server := newHTTPServer(u.cfg.HealthAddr, u.Handler())
	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("shut down uploader health server", "error", err)
		}
	}()
	defer func() {
		if err := u.Close(); err != nil {
			slog.Warn("release the spool", "error", safeError(err))
		}
	}()

	err := u.health.Start(ctx, health.Step{Name: "open the spool", Run: u.start})
	switch {
	case errors.Is(err, errNotReady):
		select {
		case <-ctx.Done():
			return nil
		case err := <-serveErr:
			return fmt.Errorf("serve uploader health checks: %w", err)
		}
	case err != nil:
		return err
	}
	slog.Info("request trace uploader ready", "backend", u.name)

	// Work runs on its own context, so a create in flight at SIGTERM can
	// finish and save its job id; the drain timeout bounds it.
	work, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()
	stop := make(chan struct{})
	// Polling runs on the status interval, apart from scans, so a slow
	// submit never holds back the confirmation of jobs already created.
	// It also runs once when the first scan has submitted what it found.
	scanned := make(chan struct{})
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		u.loop(work, stop, u.cfg.ScanInterval, u.scan, nil, func() { close(scanned) })
	}()
	go func() {
		defer loops.Done()
		u.loop(work, stop, u.pollInterval(), u.Poll, scanned, nil)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		close(stop)
		stopWork()
		loops.Wait()
		return fmt.Errorf("serve uploader health checks: %w", err)
	}

	u.health.SetReady(false)
	u.draining.Store(true)
	drainTimer := time.AfterFunc(u.cfg.DrainTimeout, stopWork)
	defer drainTimer.Stop()
	close(stop)
	loops.Wait()
	if err := u.drain(work); err != nil && work.Err() == nil {
		slog.Warn("drain request trace uploads", "error", safeError(err))
	}
	slog.Info("request trace uploader stopped", "drain_cut_short", work.Err() != nil)
	return nil
}

// loop runs run now and then on every tick until stop or ctx ends. It also
// runs once more as soon as early closes. afterFirst, when set, is called
// once the first run returns.
func (u *Uploader) loop(ctx context.Context, stop <-chan struct{}, every time.Duration, run func(context.Context) error, early <-chan struct{}, afterFirst func()) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("request trace pipeline step failed", "error", safeError(err))
		}
		if afterFirst != nil {
			afterFirst()
			afterFirst = nil
		}
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-early:
			early = nil
		case <-ticker.C:
		}
	}
}

func (u *Uploader) pollInterval() time.Duration {
	if u.cfg.StatusInterval > 0 {
		return u.cfg.StatusInterval
	}
	return config.DefaultPollInterval
}

// start runs once: create directories, check the secrets file, take the
// spool lock and load the journal, remove what another uploader left on the
// spool, and repair what a crash left in the journal.
func (u *Uploader) start(ctx context.Context) error {
	u.startOnce.Do(func() { u.startErr = u.open(ctx) })
	return u.startErr
}

func (u *Uploader) open(ctx context.Context) error {
	for _, directory := range []string{u.cfg.StateDir, u.cfg.QuarantineDir} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create uploader directory: %w", err)
		}
	}
	secret, err := os.Open(u.cfg.SecretsFile)
	if err != nil {
		return fmt.Errorf("open uploader secret file: %w", err)
	}
	if err := secret.Close(); err != nil {
		return fmt.Errorf("close uploader secret file: %w", err)
	}

	store, err := journalfs.Open(ctx, journalfs.Config{
		SpoolDir: u.cfg.SourceDir,
		StateDir: u.cfg.StateDir,
		Holder:   u.holder,
		LockWait: u.cfg.DrainTimeout + lockMargin,
	}, journalfs.Options{Counter: counterFor(u.metrics)})
	if reason := journal.NotReadyReason(err); reason != "" {
		slog.Error("request trace uploader not ready: an operator must look at the journal", "reason", reason, "error", safeError(err))
		return errNotReady
	}
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	u.store = store
	u.cleanLeftovers()
	plan, err := store.Recover(journalfs.RecoverOptions{Backend: u.name})
	if err != nil {
		return fmt.Errorf("recover the journal: %w", err)
	}
	if plan.PreparesReset > 0 || plan.BackendResets > 0 {
		slog.Info("recovered the journal", "prepares_reset", plan.PreparesReset, "backend_resets", plan.BackendResets)
	}
	if err := u.reprepareStale(); err != nil {
		return fmt.Errorf("discard transports prepared under another policy: %w", err)
	}
	u.cleaner = cleanup.New(cleanupJournal{store: store, point: u.point}, spoolIndex{dir: u.cfg.SourceDir}, cleanup.Options{Counter: counterFor(u.metrics)})
	limits := batch.Limits{
		Batching: u.caps.BatchFiles() > 1,
		MaxBytes: u.caps.MaxBatchBytes,
		MaxFiles: u.caps.BatchFiles(),
	}
	// The batch settings are defaults no operator sets, so lowering them to
	// the backend's limits is expected and not worth a warning.
	batchConfig, _ := batch.Resolve(batch.DefaultConfig(), limits)
	u.batcher, err = batch.New(batchConfig, limits, batch.Options{Metrics: batchMetrics{u.metrics}})
	return err
}

// Close releases the spool lock. The uploader does no more work after it.
func (u *Uploader) Close() error {
	for _, mu := range []*sync.Mutex{&u.intakeMu, &u.submitMu, &u.pollMu, &u.cleanupMu} {
		mu.Lock()
		defer mu.Unlock()
	}
	if u.store == nil {
		return nil
	}
	return u.store.Close()
}

type discardMetrics struct{}

func (discardMetrics) Add(string, float64, telemetry.Labels)     {}
func (discardMetrics) Set(string, float64, telemetry.Labels)     {}
func (discardMetrics) Observe(string, float64, telemetry.Labels) {}

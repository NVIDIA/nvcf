// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

// DefaultHeaderCheckRecords is how many payload records the uploader reads,
// with suppression on, before it warns that none carried a rule's header.
const DefaultHeaderCheckRecords = 100

// Options tunes an Uploader beyond its configuration.
type Options struct {
	// Metrics receives the pipeline's observations. Nil records nothing.
	Metrics telemetry.Metrics
	// HeaderCheckRecords overrides DefaultHeaderCheckRecords.
	HeaderCheckRecords int
}

// Uploader runs the export pipeline. For each closed segment it applies
// suppression and then redaction to every record, writes the records as a
// transport file, submits that file, and deletes the source once the backend
// confirms a real export.
type Uploader struct {
	cfg      config.Config
	client   backend.Client
	preparer *transform.Preparer
	metrics  telemetry.Metrics
	rules    suppressionRules
	watch    *headerWatch
	health   *health.Handler
}

// NewUploader builds the pipeline around an already-built backend.
func NewUploader(cfg config.Config, client backend.Client, opts Options) (*Uploader, error) {
	if client == nil {
		return nil, errors.New("request trace uploader needs a backend")
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
	metrics := opts.Metrics
	if metrics == nil {
		metrics = discardMetrics{}
	}
	metrics = telemetry.Safe(metrics)
	telemetry.Preset(metrics)
	rules.preset(metrics)
	threshold := opts.HeaderCheckRecords
	if threshold <= 0 {
		threshold = DefaultHeaderCheckRecords
	}
	return &Uploader{
		cfg:      cfg,
		client:   client,
		preparer: preparer,
		metrics:  metrics,
		rules:    rules,
		watch:    newHeaderWatch(preparer.SuppressionActive(), threshold, rules.names()),
		health:   health.New(),
	}, nil
}

// Handler returns the health endpoints.
func (u *Uploader) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", u.health.Live)
	mux.HandleFunc("GET /readyz", u.health.Ready)
	return mux
}

// Run serves health at once, reports ready after the local startup steps,
// and then runs a pass every scan interval until ctx ends. A failed pass is
// logged and retried on the next tick: a backend or spool fault never stops
// the process.
func (u *Uploader) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", u.cfg.HealthAddr)
	if err != nil {
		return fmt.Errorf("listen for uploader health checks: %w", err)
	}
	server := newHTTPServer(u.cfg.HealthAddr, u.Handler())
	errs := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("shut down uploader health server", "error", err)
		}
	}()

	if err := u.health.Start(ctx,
		health.Step{Name: "create local directories", Run: u.createDirectories},
		health.Step{Name: "check the secrets file", Run: u.checkSecretsFile},
	); err != nil {
		return err
	}

	ticker := time.NewTicker(u.cfg.ScanInterval)
	defer ticker.Stop()
	for {
		if err := u.Pass(ctx); err != nil && ctx.Err() == nil {
			slog.Error("request trace pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			return fmt.Errorf("serve uploader health checks: %w", err)
		case <-ticker.C:
		}
	}
}

func (u *Uploader) createDirectories(context.Context) error {
	for _, directory := range []string{u.cfg.StateDir, u.cfg.QuarantineDir} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create uploader directory: %w", err)
		}
	}
	return nil
}

func (u *Uploader) checkSecretsFile(context.Context) error {
	secret, err := os.Open(u.cfg.SecretsFile)
	if err != nil {
		return fmt.Errorf("open uploader secret file: %w", err)
	}
	return secret.Close()
}

// Pass exports every closed segment once. A segment that fails is logged
// and left for the next pass.
func (u *Uploader) Pass(ctx context.Context) error {
	scan, err := segment.ScanDir(u.cfg.SourceDir, u.cfg.Prefixes())
	if err != nil {
		return fmt.Errorf("discover request trace segments: %w", err)
	}
	for _, item := range scan.Closed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := u.export(ctx, item); err != nil {
			slog.Error("export request trace segment", "segment", item.Index, "bytes", item.Size, "error", err)
		}
	}
	return nil
}

func (u *Uploader) export(ctx context.Context, item segment.Segment) error {
	transportDir := filepath.Join(u.cfg.StateDir, "transport")
	if err := os.MkdirAll(transportDir, 0o750); err != nil {
		return fmt.Errorf("create transport directory: %w", err)
	}
	name := filepath.Base(item.Path)
	path := filepath.Join(transportDir, name)
	writer, err := transform.CreateJSONLFile(path)
	if err != nil {
		return err
	}
	rejects := transform.NewRejectFile(filepath.Join(u.cfg.QuarantineDir, "rejects"))
	result, err := u.preparer.Prepare(ctx, item.Path, writer, rejects)
	if err != nil {
		return errors.Join(fmt.Errorf("prepare segment: %w", err), rejects.Discard())
	}
	if _, err := rejects.Commit(strings.TrimSuffix(name, ".jsonl.gz")); err != nil {
		slog.Warn("keep rejected request trace lines", "segment", item.Index, "error", err)
	}
	u.recordPrepared(result)

	if result.Quarantine != "" {
		slog.Warn("request trace segment not exported", "segment", item.Index, "reason", result.Quarantine)
		return nil
	}
	caps := u.client.Capabilities()
	if result.Written == 0 {
		if caps.Exports {
			return removeSource(item.Path)
		}
		return nil
	}
	defer func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("remove request trace transport", "segment", item.Index, "error", err)
		}
	}()
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat transport: %w", err)
	}
	id, err := u.client.Submit(ctx, backend.SubmitRequest{
		Segment:     item,
		Path:        path,
		Destination: caps.AllDestinations()[0],
		Files:       []backend.File{{Path: path, Bytes: info.Size(), Rows: int64(result.Written)}},
	})
	if err != nil {
		return fmt.Errorf("submit segment to backend: %w", err)
	}
	status, err := u.client.Status(ctx, id)
	if err != nil {
		return fmt.Errorf("read backend status: %w", err)
	}
	slog.Info("submitted request trace segment", "segment", item.Index, "records", result.Written, "status", status)
	if status != backend.StatusSuccess || !caps.Exports {
		return nil
	}
	return removeSource(item.Path)
}

func removeSource(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete exported segment: %w", err)
	}
	return nil
}

type discardMetrics struct{}

func (discardMetrics) Add(string, float64, telemetry.Labels)     {}
func (discardMetrics) Set(string, float64, telemetry.Labels)     {}
func (discardMetrics) Observe(string, float64, telemetry.Labels) {}

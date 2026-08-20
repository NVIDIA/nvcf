// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package service starts the safe request-trace uploader scaffold.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/segment"

	"github.com/prometheus/client_golang/prometheus"
)

// Service owns local readiness checks, discovery metrics, and the sidecar HTTP
// server. It intentionally does not submit or delete request-trace segments.
type Service struct {
	config       config.Config
	health       *health.Handler
	metrics      *metrics.Metrics
	discovered   map[string]struct{}
	discoveredMu sync.Mutex
}

// New creates a request-trace uploader service using registry.
func New(cfg config.Config, registry *prometheus.Registry) (*Service, error) {
	m, err := metrics.New(registry)
	if err != nil {
		return nil, fmt.Errorf("create uploader metrics: %w", err)
	}
	return &Service{
		config:     cfg,
		health:     health.New(),
		metrics:    m,
		discovered: make(map[string]struct{}),
	}, nil
}

// Handler returns the service HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", s.health.Live)
	mux.HandleFunc("GET /readyz", s.health.Ready)
	mux.Handle("GET /metrics", s.metrics.Handler())
	return mux
}

// Initialize performs local, non-destructive startup checks. Remote
// reachability and backlog state do not affect readiness.
func (s *Service) Initialize() error {
	for _, directory := range []string{s.config.StateDir, s.config.QuarantineDir} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create uploader directory: %w", err)
		}
	}
	secret, err := os.Open(s.config.SecretsFile)
	if err != nil {
		return fmt.Errorf("open uploader secret file: %w", err)
	}
	if err := secret.Close(); err != nil {
		return fmt.Errorf("close uploader secret file: %w", err)
	}
	if err := s.Refresh(); err != nil {
		return fmt.Errorf("refresh local segment state: %w", err)
	}
	s.health.SetReady(true)
	return nil
}

// Refresh updates discovery and backlog metrics without changing source files.
func (s *Service) Refresh() error {
	segments, err := segment.Discover(s.config.TraceDir, s.config.TraceFilePrefix, s.config.AuditFilePrefix)
	if err != nil {
		return fmt.Errorf("discover request trace segments: %w", err)
	}
	var bytes int64
	var oldest time.Time
	for _, item := range segments {
		bytes += item.Size
		if oldest.IsZero() || item.ModTime.Before(oldest) {
			oldest = item.ModTime
		}
		s.discoveredMu.Lock()
		if _, exists := s.discovered[item.Path]; !exists {
			s.metrics.SegmentsTotal.WithLabelValues(string(item.CaptureType), "discovered").Inc()
			s.discovered[item.Path] = struct{}{}
		}
		s.discoveredMu.Unlock()
	}
	s.metrics.PendingSegments.Set(float64(len(segments)))
	s.metrics.PendingBytes.Set(float64(bytes))
	if oldest.IsZero() {
		s.metrics.OldestPendingSeconds.Set(0)
	} else {
		s.metrics.OldestPendingSeconds.Set(time.Since(oldest).Seconds())
	}
	return nil
}

// Run starts the HTTP server and periodically refreshes local discovery.
func (s *Service) Run(ctx context.Context) error {
	if err := s.Initialize(); err != nil {
		return fmt.Errorf("initialize request-trace uploader: %w", err)
	}
	server := s.httpServer()
	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	ticker := time.NewTicker(s.config.UploadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown uploader HTTP server: %w", err)
			}
			return ctx.Err()
		case err := <-errs:
			return fmt.Errorf("serve uploader HTTP endpoints: %w", err)
		case <-ticker.C:
			if err := s.Refresh(); err != nil {
				return fmt.Errorf("refresh request trace segments: %w", err)
			}
		}
	}
}

func (s *Service) httpServer() *http.Server {
	return &http.Server{
		Addr:              s.config.MetricsAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

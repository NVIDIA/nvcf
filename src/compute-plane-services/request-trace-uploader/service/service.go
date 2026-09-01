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
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// Service owns local readiness checks and the sidecar HTTP server. It
// intentionally does not submit or delete request-trace segments.
type Service struct {
	config config.Config
	health *health.Handler
}

// New creates a request-trace uploader service.
func New(cfg config.Config) (*Service, error) {
	return &Service{
		config: cfg,
		health: health.New(),
	}, nil
}

// Handler returns the service HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", s.health.Live)
	mux.HandleFunc("GET /readyz", s.health.Ready)
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

// Refresh verifies that local request-trace segment discovery succeeds without
// changing source files.
func (s *Service) Refresh() error {
	if _, err := segment.Discover(s.config.SourceDir, s.config.SegmentPrefix); err != nil {
		return fmt.Errorf("discover request trace segments: %w", err)
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

	ticker := time.NewTicker(s.config.ScanInterval)
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
		Addr:              s.config.HealthAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

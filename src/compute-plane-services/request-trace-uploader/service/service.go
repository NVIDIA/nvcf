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

	"log/slog"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// Service owns local readiness checks and the sidecar HTTP server. It
// intentionally does not submit or delete request-trace segments.
type Service struct {
	config  config.Config
	health  *health.Handler
	backend backend.Client
}

// New creates a request-trace uploader service using the configured backend.
// That backend must be linked into the build; see backend.Register.
func New(cfg config.Config) (*Service, error) {
	client, err := backend.New(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithBackend(cfg, client), nil
}

// NewWithBackend creates a service around an already-built backend. It exists
// so a caller that constructs its own backend, including a test, does not have
// to go through the registry.
func NewWithBackend(cfg config.Config, client backend.Client) *Service {
	return &Service{
		config:  cfg,
		health:  health.New(),
		backend: client,
	}
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

// Refresh submits every closed segment to the backend.
//
// Sources are never deleted here. Deletion waits on durable lifecycle state and
// confirmed terminal success, which is a later increment. A segment that fails
// is logged and left in place, so the next scan retries it.
func (s *Service) Refresh() error {
	segments, err := segment.Discover(s.config.SourceDir, s.config.SegmentPrefix)
	if err != nil {
		return fmt.Errorf("discover request trace segments: %w", err)
	}
	for _, item := range segments {
		if err := s.submit(item); err != nil {
			slog.Error("submit request trace segment",
				"segment", item.Index,
				"bytes", item.Size,
				"error", err)
		}
	}
	return nil
}

func (s *Service) submit(item segment.Segment) error {
	id, err := s.backend.Submit(context.Background(), backend.SubmitRequest{
		Segment: item,
		Path:    item.Path,
	})
	if err != nil {
		return err
	}
	status, err := s.backend.Status(context.Background(), id)
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	slog.Info("submitted request trace segment",
		"segment", item.Index,
		"bytes", item.Size,
		"status", status)
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

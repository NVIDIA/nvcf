/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
)

// ServerConfig configures the TLS http.Server that wraps the admission
// Handler. K8s API server requires HTTPS; cert + key must be valid for
// the Service DNS name (e.g. nvsnap-webhook.nvsnap-system.svc). Cert
// management itself is operator's choice (cert-manager, static Secret,
// or self-signed bootstrap) — this server just loads from disk.
type ServerConfig struct {
	// ListenAddr is the bind address (e.g. ":8443"). Default ":8443".
	ListenAddr string

	// CertFile / KeyFile are absolute paths to PEM-encoded cert + key.
	// Both required.
	CertFile string
	KeyFile  string

	// Mux path the webhook listens on. Default "/mutate".
	Path string

	// ReadTimeout / WriteTimeout for the HTTP server. Defaults: 10s / 10s.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// Log is the structured logger; nil disables logging.
	Log logrus.FieldLogger
}

// Server is a TLS http.Server that serves AdmissionReviews on cfg.Path.
//
// Lifecycle:
//
//	srv := webhook.NewServer(cfg, handler)
//	go srv.Run(ctx)        // blocks until ctx cancellation or error
//	... ctx cancel ...
//	srv.Wait()             // returns Run's error
type Server struct {
	cfg     ServerConfig
	handler *Handler

	httpSrv *http.Server
	doneCh  chan error
}

// NewServer constructs a Server. handler is wrapped at cfg.Path; other
// paths return 404. Set up the Handler's Mutator before passing in.
func NewServer(cfg ServerConfig, handler *Handler) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8443"
	}
	if cfg.Path == "" {
		cfg.Path = "/mutate"
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 10 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	mux := http.NewServeMux()
	mux.Handle(cfg.Path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return &Server{
		cfg:     cfg,
		handler: handler,
		httpSrv: &http.Server{
			Addr:         cfg.ListenAddr,
			Handler:      mux,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			TLSConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

// Run starts the TLS listener and blocks until either ctx is cancelled
// (graceful shutdown) or the server encounters a fatal error.
//
// Returns nil on graceful shutdown via ctx, or the underlying server
// error otherwise.
func (s *Server) Run(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	s.doneCh = make(chan error, 1)
	log := s.logger()
	log.WithField("addr", s.cfg.ListenAddr).WithField("path", s.cfg.Path).
		Info("webhook TLS server starting")

	go func() {
		err := s.httpSrv.ListenAndServeTLS(s.cfg.CertFile, s.cfg.KeyFile)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.doneCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		err := <-s.doneCh
		log.Info("webhook TLS server stopped")
		return err
	case err := <-s.doneCh:
		return err
	}
}

// Wait returns once Run has finished. Used by tests; production callers
// usually just block on Run.
func (s *Server) Wait() error {
	if s.doneCh == nil {
		return nil
	}
	return <-s.doneCh
}

func (s *Server) validate() error {
	if s.handler == nil {
		return errors.New("webhook.Server: handler is nil")
	}
	if s.cfg.CertFile == "" || s.cfg.KeyFile == "" {
		return errors.New("webhook.Server: CertFile and KeyFile are required")
	}
	for _, p := range []string{s.cfg.CertFile, s.cfg.KeyFile} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("webhook.Server: stat %q: %w", filepath.Base(p), err)
		}
	}
	return nil
}

func (s *Server) logger() logrus.FieldLogger {
	if s.cfg.Log != nil {
		return s.cfg.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "webhook.server")
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/config"

	"github.com/prometheus/client_golang/prometheus"
)

func TestInitializeReadinessAndDiscovery(t *testing.T) {
	root := t.TempDir()
	secretsFile := filepath.Join(root, "secrets.json")
	if err := os.WriteFile(secretsFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "request-trace.000000.jsonl.gz"), []byte("closed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "request-trace.000001.jsonl.gz"), []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		TraceDir:        root,
		TraceFilePrefix: "request-trace",
		AuditFilePrefix: "request-audit",
		SecretsFile:     secretsFile,
		StateDir:        filepath.Join(root, "state"),
		QuarantineDir:   filepath.Join(root, "quarantine"),
		MetricsAddr:     ":8011",
		UploadInterval:  config.DefaultUploadInterval,
	}
	svc, err := New(cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for path, want := range map[string]int{
		"/livez":   http.StatusOK,
		"/readyz":  http.StatusOK,
		"/metrics": http.StatusOK,
	} {
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("%s status = %d, want %d", path, response.Code, want)
		}
	}
	metricsResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(metricsResponse, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), "nvcf_dynamo_request_trace_uploader_pending_segments 1\n") {
		t.Fatalf("pending segment metric = %q, want one closed segment", metricsResponse.Body.String())
	}
	if _, err := os.Stat(cfg.StateDir); err != nil {
		t.Errorf("state directory: %v", err)
	}
	if _, err := os.Stat(cfg.QuarantineDir); err != nil {
		t.Errorf("quarantine directory: %v", err)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	svc, err := New(config.Config{MetricsAddr: ":8011"}, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := svc.httpServer()
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, 5*time.Second)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", server.ReadTimeout, 15*time.Second)
	}
	if server.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout = %v, want %v", server.WriteTimeout, 15*time.Second)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", server.IdleTimeout, 60*time.Second)
	}
}

func TestInitializeRejectsUnreadableSecret(t *testing.T) {
	root := t.TempDir()
	svc, err := New(config.Config{
		TraceDir:        root,
		TraceFilePrefix: "request-trace",
		AuditFilePrefix: "request-audit",
		SecretsFile:     filepath.Join(root, "missing.json"),
		StateDir:        filepath.Join(root, "state"),
		QuarantineDir:   filepath.Join(root, "quarantine"),
	}, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.Initialize(); err == nil {
		t.Fatal("Initialize() error = nil, want error")
	}
}

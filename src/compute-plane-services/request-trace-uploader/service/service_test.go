// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
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
		SourceDir:     root,
		SegmentPrefix: "request-trace",
		SecretsFile:   secretsFile,
		StateDir:      filepath.Join(root, "state"),
		QuarantineDir: filepath.Join(root, "quarantine"),
		HealthAddr:    ":8011",
		ScanInterval:  config.DefaultScanInterval,
	}
	svc := NewWithBackend(cfg, &stubBackend{})
	if err := svc.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for path, want := range map[string]int{
		"/livez":   http.StatusOK,
		"/readyz":  http.StatusOK,
		"/metrics": http.StatusNotFound,
	} {
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("%s status = %d, want %d", path, response.Code, want)
		}
	}
	if _, err := os.Stat(cfg.StateDir); err != nil {
		t.Errorf("state directory: %v", err)
	}
	if _, err := os.Stat(cfg.QuarantineDir); err != nil {
		t.Errorf("quarantine directory: %v", err)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	svc := NewWithBackend(config.Config{HealthAddr: ":8011"}, &stubBackend{})
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
	svc := NewWithBackend(config.Config{
		SourceDir:     root,
		SegmentPrefix: "request-trace",
		SecretsFile:   filepath.Join(root, "missing.json"),
		StateDir:      filepath.Join(root, "state"),
		QuarantineDir: filepath.Join(root, "quarantine"),
	}, &stubBackend{})
	if err := svc.Initialize(); err == nil {
		t.Fatal("Initialize() error = nil, want error")
	}
}

// stubBackend stands in for a real destination and records what it was asked
// to do, so a test can tell "the segment was submitted" apart from "nothing
// happened".
type stubBackend struct {
	submitted []string
	statuses  []string
}

func (b *stubBackend) Submit(_ context.Context, request backend.SubmitRequest) (string, error) {
	b.submitted = append(b.submitted, request.Path)
	return fmt.Sprintf("stub-%d", request.Segment.Index), nil
}

func (b *stubBackend) Status(_ context.Context, id string) (backend.Status, error) {
	b.statuses = append(b.statuses, id)
	return backend.StatusSuccess, nil
}

func TestRefreshSubmitsEveryClosedSegment(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"request-trace.000000.jsonl.gz",
		"request-trace.000001.jsonl.gz",
		"request-trace.000002.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stub := &stubBackend{}
	svc := NewWithBackend(config.Config{
		SourceDir:     root,
		SegmentPrefix: "request-trace",
		StateDir:      filepath.Join(root, "state"),
		QuarantineDir: filepath.Join(root, "quarantine"),
	}, stub)

	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	// Index 2 is the active segment and must not be submitted.
	want := []string{
		filepath.Join(root, "request-trace.000000.jsonl.gz"),
		filepath.Join(root, "request-trace.000001.jsonl.gz"),
	}
	if !reflect.DeepEqual(stub.submitted, want) {
		t.Errorf("submitted = %v, want %v", stub.submitted, want)
	}
	if !reflect.DeepEqual(stub.statuses, []string{"stub-0", "stub-1"}) {
		t.Errorf("statuses = %v, want the id from each submit", stub.statuses)
	}
	for _, path := range want {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("source %s was removed: %v", filepath.Base(path), err)
		}
	}
}

func TestRefreshStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"request-trace.000000.jsonl.gz",
		"request-trace.000001.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stub := &stubBackend{}
	svc := NewWithBackend(config.Config{
		SourceDir:     root,
		SegmentPrefix: "request-trace",
	}, stub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.Refresh(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled", err)
	}
	if len(stub.submitted) != 0 {
		t.Errorf("submitted = %v, want nothing after cancellation", stub.submitted)
	}
}

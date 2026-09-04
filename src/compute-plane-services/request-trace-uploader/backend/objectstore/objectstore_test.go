// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

func writeSecrets(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSegment(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseConfig(secretsFile, endpoint string) config.Config {
	return config.Config{
		Backend:     config.BackendObjectStore,
		SecretsFile: secretsFile,
		ObjectStore: config.ObjectStorePolicy{
			Bucket:    "request-traces",
			Region:    "us-east-1",
			Endpoint:  endpoint,
			PathStyle: true,
		},
	}
}

func TestNewRequiresBucket(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	cfg.ObjectStore.Bucket = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a missing-bucket error")
	}
}

func TestNewRequiresRegion(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	cfg.ObjectStore.Region = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a missing-region error")
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{}`), "https://127.0.0.1:0")
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a missing-credentials error")
	}
}

func TestNewRejectsUnreadableSecretsFile(t *testing.T) {
	cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), "https://127.0.0.1:0")
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a read error")
	}
}

// TestNewRejectsAnHTTPEndpoint guards against a directly constructed
// config.Config bypassing the https-only check in config.Load: New must
// enforce the same policy so credentials and segment data are never sent in
// cleartext, regardless of how the caller built its Config.
func TestNewRejectsAnHTTPEndpoint(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "http://127.0.0.1:0")
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want an http endpoint to be rejected")
	}
}

func TestRegisteredUnderObjectStoreBackend(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	client, err := backend.New(cfg)
	if err != nil {
		t.Fatalf("backend.New() error = %v, want the objectstore backend to be registered", err)
	}
	if client == nil {
		t.Fatal("backend.New() returned a nil client")
	}
}

func TestSubmitUploadsAndReportsSuccess(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b","session_token":"c"}`), server.URL)
	cfg.ObjectStore.KeyPrefix = "segments"
	client, err := newClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "compressed-fixture")
	id, err := client.(backend.Client).Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id != "segments/request-trace.000000.jsonl.gz" {
		t.Errorf("Submit() id = %q, want the prefixed object key", id)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/request-traces/segments/request-trace.000000.jsonl.gz" {
		t.Errorf("path = %q, want the bucket and key", gotPath)
	}
	if gotContentType != "application/gzip" {
		t.Errorf("content type = %q, want application/gzip", gotContentType)
	}
	if string(gotBody) != "compressed-fixture" {
		t.Errorf("body = %q, want the segment contents", gotBody)
	}

	status, err := client.(backend.Client).Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != backend.StatusSuccess {
		t.Errorf("Status() = %v, want success", status)
	}
}

func TestSubmitWithoutKeyPrefixUsesTheSegmentName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	client, err := newClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	id, err := client.(backend.Client).Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id != "request-trace.000000.jsonl.gz" {
		t.Errorf("Submit() id = %q, want the bare segment name", id)
	}
}

func TestSubmitFailsOnAMissingSegment(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.(backend.Client).Submit(context.Background(), backend.SubmitRequest{Path: "/nonexistent/request-trace.000000.jsonl.gz"})
	if err == nil {
		t.Fatal("Submit() error = nil, want a stat failure")
	}
}

func TestSubmitReturnsTheStoreError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	client, err := newClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	_, err = client.(backend.Client).Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err == nil {
		t.Fatal("Submit() error = nil, want the store's rejection")
	}
}

func TestSubmitStopsOnCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	client, err := newClient(cfg, server.Client())
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := writeSegment(t, "fixture")
	_, err = client.(backend.Client).Submit(ctx, backend.SubmitRequest{Path: path})
	if err == nil {
		t.Fatal("Submit() error = nil, want cancellation to fail the upload")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestCapabilitiesDeclareExportAndSyncOutcome(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	caps := client.(backend.Client).Capabilities()
	if !caps.Exports {
		t.Error("Capabilities().Exports = false, want true")
	}
	if !caps.TerminalOutcomeSync {
		t.Error("Capabilities().TerminalOutcomeSync = false, want true")
	}
	if !caps.ResubmitSafe {
		t.Error("Capabilities().ResubmitSafe = false, want true")
	}
	if caps.MaxObjectBytes != maxObjectBytes {
		t.Errorf("Capabilities().MaxObjectBytes = %d, want %d", caps.MaxObjectBytes, maxObjectBytes)
	}
}

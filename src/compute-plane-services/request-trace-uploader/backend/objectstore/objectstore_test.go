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

// withHostname pins the package-level hostname lookup for a test, so object
// keys are deterministic and a test can simulate two distinct uploader
// instances.
func withHostname(t *testing.T, name string) {
	t.Helper()
	original := hostname
	hostname = func() (string, error) { return name, nil }
	t.Cleanup(func() { hostname = original })
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

// TestNewRejectsAnHTTPSEndpointWithNoHost guards against a scheme-only
// endpoint: "https://" has the right prefix but no host, so a prefix check
// alone would accept it and newClient would only fail later, deep in the SDK.
func TestNewRejectsAnHTTPSEndpointWithNoHost(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://")
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a hostless endpoint to be rejected")
	}
}

func TestNewFailsWhenHostnameIsUnavailable(t *testing.T) {
	original := hostname
	hostname = func() (string, error) { return "", errors.New("no hostname") }
	t.Cleanup(func() { hostname = original })

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	if _, err := New(cfg); err == nil {
		t.Fatal("New() error = nil, want a hostname lookup failure to be reported")
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
	withHostname(t, "pod-a")
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
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "compressed-fixture")
	id, err := client.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id != "segments/pod-a/request-trace.000000.jsonl.gz" {
		t.Errorf("Submit() id = %q, want the prefixed, hostname-namespaced object key", id)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/request-traces/segments/pod-a/request-trace.000000.jsonl.gz" {
		t.Errorf("path = %q, want the bucket and key", gotPath)
	}
	if gotContentType != "application/gzip" {
		t.Errorf("content type = %q, want application/gzip", gotContentType)
	}
	if string(gotBody) != "compressed-fixture" {
		t.Errorf("body = %q, want the segment contents", gotBody)
	}

	status, err := client.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != backend.StatusSuccess {
		t.Errorf("Status() = %v, want success", status)
	}
}

func TestSubmitWithoutKeyPrefixNamespacesByHostname(t *testing.T) {
	withHostname(t, "pod-a")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	id, err := client.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id != "pod-a/request-trace.000000.jsonl.gz" {
		t.Errorf("Submit() id = %q, want the hostname-namespaced segment name", id)
	}
}

// TestSubmitKeysDoNotCollideAcrossInstances guards the scenario CodeRabbit
// flagged: two uploader instances that discover the same segment file name
// (segment.Discover indexes restart from zero independently per instance)
// must not overwrite each other's object when they share a bucket and
// prefix.
func TestSubmitKeysDoNotCollideAcrossInstances(t *testing.T) {
	var gotKeys []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeys = append(gotKeys, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secretsFile := writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`)
	path := writeSegment(t, "fixture")

	withHostname(t, "pod-a")
	cfgA := baseConfig(secretsFile, server.URL)
	clientA, err := newClient(cfgA, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	idA, err := clientA.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	withHostname(t, "pod-b")
	cfgB := baseConfig(secretsFile, server.URL)
	clientB, err := newClient(cfgB, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	idB, err := clientB.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	if idA == idB {
		t.Fatalf("two instances produced the same object key %q, want distinct keys", idA)
	}
	if len(gotKeys) == 2 && gotKeys[0] == gotKeys[1] {
		t.Fatalf("both uploads targeted the same object path %q, want distinct paths", gotKeys[0])
	}
}

func TestSubmitFailsOnAMissingSegment(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Submit(context.Background(), backend.SubmitRequest{Path: "/nonexistent/request-trace.000000.jsonl.gz"})
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
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	_, err = client.Submit(context.Background(), backend.SubmitRequest{Path: path})
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
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := writeSegment(t, "fixture")
	_, err = client.Submit(ctx, backend.SubmitRequest{Path: path})
	if err == nil {
		t.Fatal("Submit() error = nil, want cancellation to fail the upload")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestSubmitRejectsAnHTTPSToHTTPRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:9/evil")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	_, err = client.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err == nil {
		t.Fatal("Submit() error = nil, want a 307 redirect to an http URL to be rejected")
	}
}

func TestCapabilitiesDeclareExportAndSyncOutcome(t *testing.T) {
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), "https://127.0.0.1:0")
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	caps := client.Capabilities()
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

// TestNewDryRunSkipsCredentials confirms a dry run needs no secrets file: it
// never authenticates to a store, so requiring credentials would defeat the
// point of testing config and key computation without one.
func TestNewDryRunSkipsCredentials(t *testing.T) {
	cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), "https://127.0.0.1:0")
	cfg.ObjectStore.DryRun = true
	if _, err := New(cfg); err != nil {
		t.Fatalf("New() error = %v, want a dry run to skip the credentials file", err)
	}
}

// TestNewDryRunStillRequiresBucketRegionAndHTTPS confirms a dry run still
// validates the settings a real upload would need, so it exercises the same
// configuration surface rather than a reduced one.
func TestNewDryRunStillRequiresBucketRegionAndHTTPS(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "missing bucket", mutate: func(c *config.Config) { c.ObjectStore.Bucket = "" }},
		{name: "missing region", mutate: func(c *config.Config) { c.ObjectStore.Region = "" }},
		{name: "http endpoint", mutate: func(c *config.Config) { c.ObjectStore.Endpoint = "http://127.0.0.1:0" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), "https://127.0.0.1:0")
			cfg.ObjectStore.DryRun = true
			tt.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("New() error = nil, want dry run to still validate this setting")
			}
		})
	}
}

func TestSubmitDryRunLogsAndDoesNotUpload(t *testing.T) {
	withHostname(t, "pod-a")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("dry run must not contact the store")
	}))
	defer server.Close()

	cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), server.URL)
	cfg.ObjectStore.KeyPrefix = "segments"
	cfg.ObjectStore.DryRun = true
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	path := writeSegment(t, "fixture")
	id, err := client.Submit(context.Background(), backend.SubmitRequest{Path: path})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id != "segments/pod-a/request-trace.000000.jsonl.gz" {
		t.Errorf("Submit() id = %q, want the computed object key", id)
	}

	status, err := client.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != backend.StatusSuccess {
		t.Errorf("Status() = %v, want success", status)
	}
}

func TestSubmitDryRunFailsOnAMissingSegment(t *testing.T) {
	cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), "https://127.0.0.1:0")
	cfg.ObjectStore.DryRun = true
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Submit(context.Background(), backend.SubmitRequest{Path: "/nonexistent/request-trace.000000.jsonl.gz"})
	if err == nil {
		t.Fatal("Submit() error = nil, want a stat failure even in dry-run mode")
	}
}

// TestCapabilitiesDryRunDoesNotExport guards the deletion decision in
// service.Refresh: a dry run must report Exports=false so a caller never
// deletes a source segment on the strength of a dry-run Submit succeeding.
func TestCapabilitiesDryRunDoesNotExport(t *testing.T) {
	cfg := baseConfig(filepath.Join(t.TempDir(), "missing.json"), "https://127.0.0.1:0")
	cfg.ObjectStore.DryRun = true
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.Capabilities().Exports {
		t.Error("Capabilities().Exports = true, want false for a dry run")
	}
}

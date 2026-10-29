// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package objectstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// TestObjectKeyNamesTheSourceSegmentNotItsTransport pins the object key
// format: <prefix>/<host>/<source segment file name>. The file uploaded is
// a prepared transport whose own name is internal; the key must not change
// with it.
func TestObjectKeyNamesTheSourceSegmentNotItsTransport(t *testing.T) {
	withHostname(t, "pod-a")
	var gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := baseConfig(writeSecrets(t, `{"access_key_id":"a","secret_access_key":"b"}`), server.URL)
	cfg.ObjectStore.KeyPrefix = "segments"
	client, err := newClient(cfg, server.Client().Transport)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	transport := filepath.Join(t.TempDir(), "3b1f0c2d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9.0123456789abcdef.jsonl.gz")
	if err := os.WriteFile(transport, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Path: "/spool/request-trace.000003.jsonl.gz", Prefix: "request-trace", Index: 3},
		Path:    transport,
		Files:   []backend.File{{Path: transport, Bytes: 8}},
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if want := "segments/pod-a/request-trace.000003.jsonl.gz"; id != want {
		t.Errorf("Submit() id = %q, want %q", id, want)
	}
	if want := "/request-traces/segments/pod-a/request-trace.000003.jsonl.gz"; gotPath != want {
		t.Errorf("object path = %q, want %q", gotPath, want)
	}
}

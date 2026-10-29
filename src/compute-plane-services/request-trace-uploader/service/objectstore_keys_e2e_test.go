// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// TestObjectStoreKeysKeepTheSourceSegmentNames checks the public object
// layout through the pipeline: each segment lands at
// <bucket>/<host>/<source segment file name>, as before the pipeline wrote
// prepared transports.
func TestObjectStoreKeysKeepTheSourceSegmentNames(t *testing.T) {
	store := &objectStore{objects: map[string][]byte{}}
	server := httptest.NewTLSServer(store)
	t.Cleanup(server.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	spool, _ := spoolWith(t,
		[]string{uploadertest.RequestEnd("a", 1)},
		[]string{uploadertest.RequestEnd("b", 2)},
	)
	env := spoolEnv(t, spool.Dir, map[string]string{
		config.EnvBackend:              string(config.BackendObjectStore),
		config.EnvObjectStoreBucket:    "traces",
		config.EnvObjectStoreRegion:    "us-east-1",
		config.EnvObjectStoreEndpoint:  server.URL,
		config.EnvObjectStorePathStyle: "true",
	})
	if err := os.WriteFile(env[config.EnvSecretsFile], []byte(`{"access_key_id":"test","secret_access_key":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := backend.New(loadEnv(t, env))
	if err != nil {
		t.Fatalf("backend.New() error = %v", err)
	}
	uploader := newUploader(t, spool, client, service.Options{}, env)
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	store.mu.Lock()
	for key := range store.objects {
		keys = append(keys, key)
	}
	store.mu.Unlock()
	slices.Sort(keys)
	want := []string{
		"/traces/" + host + "/" + config.DefaultSegmentPrefix + ".000000.jsonl.gz",
		"/traces/" + host + "/" + config.DefaultSegmentPrefix + ".000001.jsonl.gz",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("object keys = %q, want %q", keys, want)
	}
}

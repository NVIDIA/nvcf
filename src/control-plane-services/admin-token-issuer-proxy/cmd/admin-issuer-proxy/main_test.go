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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
)

func TestTerminationContextHandlesSIGTERM(t *testing.T) {
	ctx, stop := terminationContext(context.Background())
	defer stop()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find current process: %v", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("termination context was not canceled by SIGTERM")
	}
}

func TestMetadataInitializationErrorHandling(t *testing.T) {
	t.Run("cancellation invokes graceful shutdown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		shutdownCalled := make(chan struct{})
		server := &http.Server{}
		server.RegisterOnShutdown(func() { close(shutdownCalled) })
		serverErr := make(chan error, 1)
		serverErr <- http.ErrServerClosed

		if err := handleMetadataInitializationError(ctx, context.Canceled, server, serverErr); err != nil {
			t.Fatalf("handleMetadataInitializationError() returned %v", err)
		}
		select {
		case <-shutdownCalled:
		case <-time.After(time.Second):
			t.Fatal("graceful shutdown was not invoked")
		}
	})

	t.Run("non-cancellation error remains fatal", func(t *testing.T) {
		metadataErr := errors.New("invalid metadata")
		server := &http.Server{}
		serverErr := make(chan error)

		err := handleMetadataInitializationError(t.Context(), metadataErr, server, serverErr)
		if !errors.Is(err, metadataErr) {
			t.Fatalf("handleMetadataInitializationError() error = %v, want wrapped metadata error", err)
		}
	})
}

func TestRunStaysLiveUntilMetadataIsReady(t *testing.T) {
	var metadataReady atomic.Bool
	var metadataRequests atomic.Int32
	metadataUnavailable := make(chan struct{}, 1)
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataRequests.Add(1)
		if !metadataReady.Load() {
			select {
			case metadataUnavailable <- struct{}{}:
			default:
			}
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(models.ServicesResponse{Services: []models.ServiceInfo{{
			ServiceID:   "nvcf-api",
			ServiceName: "nvcf-api",
		}}})
	}))
	defer metadataServer.Close()

	var vaultRequests atomic.Int32
	vaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultRequests.Add(1)
		http.Error(w, "unexpected Vault request", http.StatusInternalServerError)
	}))
	defer vaultServer.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	cfg := &config.Config{
		VaultAddr:          vaultServer.URL,
		SignPath:           "services/example/jwt/sign",
		Role:               "admin-issuer-proxy",
		VaultTokenFile:     tokenFile,
		ListenAddr:         listener.Addr().String(),
		ServiceMetadataURL: metadataServer.URL,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, cfg, listener)
	}()

	baseURL := "http://" + listener.Addr().String()
	waitForStatus(t, http.MethodGet, baseURL+"/healthz", http.StatusOK)
	waitForStatus(t, http.MethodGet, baseURL+"/readyz", http.StatusServiceUnavailable)
	waitForStatus(t, http.MethodPost, baseURL+"/v1/admin/keys", http.StatusServiceUnavailable)
	if got := vaultRequests.Load(); got != 0 {
		t.Fatalf("Vault received %d requests before readiness", got)
	}
	select {
	case <-metadataUnavailable:
	case <-time.After(time.Second):
		t.Fatal("metadata dependency did not receive the initial unavailable request")
	}

	metadataReady.Store(true)
	waitForStatus(t, http.MethodGet, baseURL+"/readyz", http.StatusOK)
	if got := metadataRequests.Load(); got < 2 {
		t.Fatalf("metadata requests = %d, want at least 2 to prove retry", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	// shutdown has a five-second grace period, so keep the test deadline above
	// the implementation's bound when the host is under concurrent build load.
	case <-time.After(6 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
}

func waitForStatus(t *testing.T, method, url string, want int) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s %s did not return %d", method, url, want)
}

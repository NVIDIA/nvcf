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
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/handlers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/platform/vault"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/servicecache"
)

const (
	serviceMetadataInitialBackoff = time.Second
	serviceMetadataMaxBackoff     = 10 * time.Second
)

// Version information (set via ldflags during build)
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	// Parse flags
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("admin-issuer-proxy version %s\n", Version)
		fmt.Printf("  Build time: %s\n", BuildTime)
		fmt.Printf("  Git commit: %s\n", GitCommit)
		os.Exit(0)
	}

	log.Printf("Starting admin-issuer-proxy version %s (commit: %s)", Version, GitCommit)

	// Load configuration from environment variables
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Verify we can read the Vault token at startup; crash fast if not present.
	if _, err := vault.ReadTokenFile(cfg.VaultTokenFile); err != nil {
		log.Fatalf("Vault token not found or unreadable: %v", err)
	}

	ctx, stop := terminationContext(context.Background())
	defer stop()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", cfg.ListenAddr, err)
	}
	if err := run(ctx, cfg, listener); err != nil {
		log.Fatalf("Admin-Issuer-Proxy stopped: %v", err)
	}
}

func terminationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// run serves health and token endpoints while service metadata initializes.
// The process stays live during retryable API Keys cold starts, but readiness
// and token issuance remain unavailable until the cache is populated.
func run(ctx context.Context, cfg *config.Config, listener net.Listener) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cache := servicecache.New(cfg.ServiceMetadataURL)
	h := handlers.New(cfg, cache)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.Health)
	mux.HandleFunc("/readyz", h.Ready)
	mux.HandleFunc("/v1/admin/keys", h.Keys)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	defer func() { _ = server.Close() }()

	log.Printf("Admin-Issuer-Proxy listening on %s", listener.Addr())
	log.Printf("Configuration: Vault=%s, SignPath=%s, Role=%s, TokenFile=%s",
		cfg.VaultAddr, cfg.SignPath, cfg.Role, cfg.VaultTokenFile)

	metadataErr := make(chan error, 1)
	go func() {
		log.Printf("Fetching service metadata from %s...", cfg.ServiceMetadataURL)
		metadataErr <- cache.FetchWithRetry(runCtx, servicecache.RetryPolicy{
			InitialBackoff: serviceMetadataInitialBackoff,
			MaxBackoff:     serviceMetadataMaxBackoff,
			OnRetry: func(err error, nextDelay time.Duration) {
				log.Printf("Service metadata temporarily unavailable: %v; retrying in %s", err, nextDelay)
			},
		})
	}()

	select {
	case err := <-metadataErr:
		if err != nil {
			return handleMetadataInitializationError(runCtx, err, server, serverErr)
		}
		serviceInfo := cache.Get()
		log.Printf("Service metadata cached: %s (ID: %s)", serviceInfo.ServiceName, serviceInfo.ServiceID)
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-runCtx.Done():
		return shutdown(server, serverErr)
	}

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-runCtx.Done():
		return shutdown(server, serverErr)
	}
}

func handleMetadataInitializationError(
	ctx context.Context,
	err error,
	server *http.Server,
	serverErr <-chan error,
) error {
	if ctx.Err() != nil {
		return shutdown(server, serverErr)
	}
	return fmt.Errorf("failed to initialize service metadata: %w", err)
}

func shutdown(server *http.Server, serverErr <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down server: %w", err)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error during shutdown: %w", err)
	}
	return nil
}

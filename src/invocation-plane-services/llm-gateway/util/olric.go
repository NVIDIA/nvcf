/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package util

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olric-data/olric"
	olricconfig "github.com/olric-data/olric/config"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
	"github.com/NVIDIA/nvcf/llm-api-gateway/telemetry"
)

// DefaultOlricShutdownTimeout is the fallback deadline used by ShutdownNode
// when neither the passed-in timeout nor OlricConfig.ShutdownTimeout is set.
const DefaultOlricShutdownTimeout = 5 * time.Second

// OlricNode bundles a running embedded Olric instance with its DMap handle
// and a Shutdown function. Callers must call Shutdown on process exit to leave
// the cluster cleanly.
type OlricNode struct {
	DB       *olric.Olric
	Client   *olric.EmbeddedClient
	DMap     olric.DMap
	Shutdown func(ctx context.Context) error
}

// NewOlricNode starts an embedded Olric node using the supplied configuration
// and blocks until the node is ready to accept local operations. Peers, if
// provided, are used to join an existing cluster; an empty peer list yields a
// single-node cluster which is useful for local development and tests.
//
// ctx controls the startup deadline and attaches to the readiness log. The
// embedded node itself continues to run past ctx cancellation; use the
// returned Shutdown function to stop it.
func NewOlricNode(ctx context.Context, cfg config.OlricConfig) (*OlricNode, error) {
	log := telemetry.Logger(ctx)

	oc := olricconfig.New(defaultEnv(cfg.Environment))

	if cfg.BindAddr != "" {
		oc.BindAddr = cfg.BindAddr
	}
	if cfg.BindPort != 0 {
		oc.BindPort = cfg.BindPort
	}
	if cfg.MemberlistBindAddr != "" {
		oc.MemberlistConfig.BindAddr = cfg.MemberlistBindAddr
	}
	if cfg.MemberlistBindPort != 0 {
		oc.MemberlistConfig.BindPort = cfg.MemberlistBindPort
		oc.MemberlistConfig.AdvertisePort = cfg.MemberlistBindPort
	}
	discoveryMode, err := configureDiscovery(oc, cfg)
	if err != nil {
		return nil, fmt.Errorf("configure olric discovery: %w", err)
	}
	if cfg.ReplicaCount > 0 {
		oc.ReplicaCount = cfg.ReplicaCount
	}
	if cfg.PartitionCount > 0 {
		oc.PartitionCount = cfg.PartitionCount
	}
	if cfg.LogOutput != nil {
		oc.LogOutput = cfg.LogOutput
	}
	if cfg.LogVerbosity > 0 {
		oc.LogVerbosity = cfg.LogVerbosity
	}
	if cfg.LogLevel != "" {
		oc.LogLevel = cfg.LogLevel
	}

	startupTimeout := cfg.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 15 * time.Second
	}
	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultOlricShutdownTimeout
	}

	ready := make(chan struct{})
	oc.Started = func() {
		close(ready)
	}

	db, err := olric.New(oc)
	if err != nil {
		return nil, fmt.Errorf("create olric node: %w", err)
	}

	// Buffered so Start() can always deliver its terminal error without
	// blocking on a reader. We read from startErr on the startup-failure and
	// timeout paths; on the happy path the goroutine holds the error until
	// db.Shutdown unblocks Start, which is fine - the buffered channel
	// prevents a goroutine leak.
	startErr := make(chan error, 1)
	go func() {
		startErr <- db.Start()
	}()

	select {
	case <-ready:
	case err := <-startErr:
		if err == nil {
			err = fmt.Errorf("olric start returned before node became ready")
		}
		return nil, fmt.Errorf("start olric node: %w", err)
	case <-time.After(startupTimeout):
		shutdownNode(ctx, db, shutdownTimeout)
		return nil, fmt.Errorf("olric node did not become ready within %s", startupTimeout)
	}

	log.Info().
		Str("bind_addr", oc.BindAddr).
		Int("bind_port", oc.BindPort).
		Str("peers", strings.Join(oc.Peers, ",")).
		Str("discovery", discoveryMode).
		Msg("olric node ready")

	client := db.NewEmbeddedClient()

	dmapName := cfg.DMapName
	if dmapName == "" {
		dmapName = "rate-limit"
	}
	dm, err := client.NewDMap(dmapName)
	if err != nil {
		shutdownNode(ctx, db, shutdownTimeout)
		return nil, fmt.Errorf("create olric dmap %q: %w", dmapName, err)
	}

	return &OlricNode{
		DB:     db,
		Client: client,
		DMap:   dm,
		Shutdown: func(shutdownCtx context.Context) error {
			shutdownLog := telemetry.Logger(shutdownCtx)
			if err := client.Close(shutdownCtx); err != nil {
				shutdownLog.Warn().Err(err).Msg("failed to close olric embedded client")
			}
			return db.Shutdown(shutdownCtx)
		},
	}, nil
}

// ShutdownOlricNode stops an Olric node with a bounded deadline, logging any
// error. It is safe to pass nil. The timeout falls back to
// DefaultOlricShutdownTimeout when zero.
//
// Use this from process shutdown hooks and error-handling paths that would
// otherwise duplicate the `context.WithTimeout` + `Shutdown` + swallow-log
// pattern across packages.
func ShutdownOlricNode(ctx context.Context, node *OlricNode, timeout time.Duration) {
	if node == nil || node.Shutdown == nil {
		return
	}
	if timeout <= 0 {
		timeout = DefaultOlricShutdownTimeout
	}
	log := telemetry.Logger(ctx)
	shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := node.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("failed to shutdown olric node")
	}
}

// shutdownNode is the internal counterpart used from inside NewOlricNode,
// where the node has been partially built (the *olric.Olric exists but we
// never wrapped it in an OlricNode, so ShutdownOlricNode cannot take over).
func shutdownNode(ctx context.Context, db *olric.Olric, timeout time.Duration) {
	shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.Shutdown(shutdownCtx); err != nil {
		telemetry.Logger(shutdownCtx).
			Warn().
			Err(err).
			Msg("failed to shutdown olric node during startup error handling")
	}
}

func defaultEnv(env string) string {
	switch strings.ToLower(env) {
	case "lan":
		return "lan"
	case "wan":
		return "wan"
	default:
		return "local"
	}
}

// configureDiscovery picks the peer-discovery mechanism for an Olric node and
// mutates oc accordingly. It returns a short human-readable label for logs.
//
// Precedence:
//  1. Static peers: cfg.Peers is non-empty. Used by tests (they set Peers
//     programmatically) and by deployments that still prefer a seed list.
//  2. Kubernetes API discovery: POD_NAMESPACE is set in the environment.
//     This is the normal production path; the Downward API wires POD_NAMESPACE
//     into the pod, which is our signal that we're running in-cluster.
//  3. Single-node: nothing configured. Used by local `go run ./cmd/...`
//     without any config, where the process forms a 1-node cluster.
//
// This is intentionally auto-detecting rather than an explicit knob: tests
// don't set POD_NAMESPACE and always provide Peers, production does the
// opposite, and local dev does neither.
func configureDiscovery(oc *olricconfig.Config, cfg config.OlricConfig) (string, error) {
	if len(cfg.Peers) > 0 {
		oc.Peers = append(oc.Peers, cfg.Peers...)
		return "static", nil
	}

	if ns := os.Getenv(PodNamespaceEnv); ns != "" {
		plugin, err := NewK8sDiscovery(context.Background(), cfg.K8sLabelSelector)
		if err != nil {
			return "", fmt.Errorf("kubernetes discovery: %w", err)
		}
		if oc.ServiceDiscovery == nil {
			oc.ServiceDiscovery = make(map[string]any)
		}
		oc.ServiceDiscovery["plugin"] = plugin
		return "kubernetes", nil
	}

	return "single-node", nil
}

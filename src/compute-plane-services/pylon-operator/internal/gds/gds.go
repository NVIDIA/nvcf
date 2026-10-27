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

// Package gds watches the routers of the clusterset in the Global Discovery
// Service (GDS) with --router-source=gds. Transport pods find their routers
// themselves; the operator uses the router list only for a startup warning
// when none is visible and for status.registration.routers.
package gds

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	gdsclient "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/version"
)

// DefaultSyncTimeout bounds the wait for the first router list before the
// startup check reports GDS as unreachable.
const DefaultSyncTimeout = time.Minute

// ErrNoRouters is logged when GDS lists no router for the clusterset.
var ErrNoRouters = errors.New("no router with a live lease and ready replicas in the clusterset")

// Options tune Routers. Zero values take the defaults.
type Options struct {
	// Registerer receives the nvcf_gds_client_* metrics. Nil registers
	// nothing.
	Registerer prometheus.Registerer
	// Logger receives the startup check and the client's failover logs.
	Logger logr.Logger
	// SyncTimeout bounds the wait for the first router list.
	SyncTimeout time.Duration
	// TTL is the router cache's full re-list interval.
	TTL time.Duration
}

// Routers keeps a router cache of the clusterset.
type Routers struct {
	client      *gdsclient.Client
	cache       *gdsclient.RouterCache
	clusterset  string
	syncTimeout time.Duration
	log         logr.Logger
}

// New connects to GDS with the configured URLs. caPEM is the GDS CA bundle,
// trusted in addition to the system roots; nil trusts the system roots
// only. The client connects in the background.
func New(ctx context.Context, cfg config.Config, caPEM []byte, opts Options) (*Routers, error) {
	tlsConfig, err := TLSConfig(caPEM)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log.GetSink() == nil {
		log = logr.Discard()
	}
	c, err := gdsclient.New(ctx, gdsclient.Config{
		URLs:              cfg.GDSURLs,
		TLS:               tlsConfig,
		AllowInsecureHTTP: plaintext(cfg.GDSURLs),
		UserAgent:         "pylon-operator/" + version.Version,
		Registerer:        opts.Registerer,
		Logger:            slog.New(logr.ToSlogHandler(log)),
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to GDS: %w", err)
	}
	cache, err := gdsclient.NewRouterCache(c, gdsclient.RouterCacheConfig{
		Filter: &gdsv1.ListRoutersRequest{Clusterset: cfg.GDSClustersetID},
		TTL:    opts.TTL,
	})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("creating the GDS router cache: %w", err)
	}
	syncTimeout := opts.SyncTimeout
	if syncTimeout <= 0 {
		syncTimeout = DefaultSyncTimeout
	}
	return &Routers{
		client:      c,
		cache:       cache,
		clusterset:  cfg.GDSClustersetID,
		syncTimeout: syncTimeout,
		log:         log,
	}, nil
}

// plaintext reports whether the GDS URLs use http. config.Validate requires
// one scheme for all of them.
func plaintext(urls []string) bool {
	return len(urls) > 0 && strings.HasPrefix(urls[0], "http://")
}

// Start runs the router cache until ctx ends, checks once that a router is
// visible, and closes the client. It implements manager.Runnable; the
// manager runs it on the leader, which writes status.
func (r *Routers) Start(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.checkStartup(ctx)
	}()
	err := r.cache.Run(ctx)
	<-done
	if closeErr := r.client.Close(); err == nil {
		err = closeErr
	}
	return err
}

// RouterCount is the number of routers with a live lease and ready
// replicas, the routers transport pods can register with, and whether the
// router list is known yet.
func (r *Routers) RouterCount() (int32, bool) {
	s := r.cache.Snapshot()
	if s == nil || !s.Synced {
		return 0, false
	}
	return int32(len(s.SeedRouters())), true
}

// checkStartup waits for the first router list and logs an error when GDS
// cannot be reached in time or lists no router: transport pods then have
// nothing to register with.
func (r *Routers) checkStartup(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, r.syncTimeout)
	defer cancel()
	if err := r.cache.WaitSynced(wctx); err != nil {
		if ctx.Err() == nil {
			r.log.Error(err, "Could not list routers from GDS; transport pods keep retrying",
				"clusterset", r.clusterset, "timeout", r.syncTimeout.String(), "lastError", fmt.Sprint(r.cache.LastError()))
		}
		return
	}
	n, _ := r.RouterCount()
	if n == 0 {
		r.log.Error(ErrNoRouters, "GDS lists no router; transport pods have nothing to register with until a router cluster exports one",
			"clusterset", r.clusterset)
		return
	}
	r.log.Info("GDS lists routers for the clusterset", "clusterset", r.clusterset, "routers", n)
}

// TLSConfig trusts the system roots plus the PEM certificates in caPEM. A
// nil or empty caPEM returns nil, the client's default.
func TLSConfig(caPEM []byte) (*tls.Config, error) {
	if len(caPEM) == 0 {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("the GDS CA bundle holds no PEM certificate")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil
}

// LoadCA reads the ca.crt key of the ConfigMap name in namespace, the GDS CA
// bundle. An empty name returns nil. The content is never logged.
func LoadCA(ctx context.Context, reader client.Reader, namespace, name string) ([]byte, error) {
	if name == "" {
		return nil, nil
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := reader.Get(ctx, key, cm); err != nil {
		return nil, fmt.Errorf("reading the GDS CA ConfigMap %s: %w", key, err)
	}
	if pem := cm.Data[transport.TrustBundleCAKey]; pem != "" {
		return []byte(pem), nil
	}
	if pem := cm.BinaryData[transport.TrustBundleCAKey]; len(pem) > 0 {
		return pem, nil
	}
	return nil, fmt.Errorf("the GDS CA ConfigMap %s has no %s key", key, transport.TrustBundleCAKey)
}

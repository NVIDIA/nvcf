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

package server

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.uber.org/zap"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/auth"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/clock"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/instrumented"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/version"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

// AppOptions are the dependencies of an App that do not come from the
// configuration.
type AppOptions struct {
	// Store is the backend; App wraps it with metrics and spans.
	Store  store.Store
	Logger *zap.Logger
	// Registry defaults to a new registry with the Go and process
	// collectors.
	Registry *prometheus.Registry
	// Clock defaults to the system clock.
	Clock         clock.Clock
	Authenticator auth.Authenticator
}

// App is a fully wired gds-server: store, view, hub, refresher, compactor,
// API and listeners.
type App struct {
	Server   *Server
	Store    store.Store
	View     *view.View
	Hub      *view.Hub
	Metrics  *metrics.Metrics
	Registry *prometheus.Registry
}

// NewApp wires a gds-server from its configuration and binds its listeners.
func NewApp(cfg *config.Config, opts AppOptions) (*App, error) {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	reg := opts.Registry
	if reg == nil {
		reg = prometheus.NewRegistry()
		reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	}
	m := metrics.New(reg)
	m.SetBuildInfo(version.Version, version.GitHash)
	st := instrumented.New(opts.Store, m)
	log := opts.Logger

	v := view.New(view.Options{
		Clock:            opts.Clock,
		LeaseSkew:        cfg.LeaseSkew,
		WatchHistory:     cfg.WatchHistory,
		TombstoneTTL:     cfg.BookmarkInterval,
		LocalWriteWindow: cfg.RefreshInterval,
		Metrics:          m,
		Logger:           log.With(zap.String("component", "view")),
	})
	hub := view.NewHub(v, view.HubOptions{
		Clock:            opts.Clock,
		BookmarkInterval: cfg.BookmarkInterval,
		QueueSize:        cfg.WatchQueueSize,
		MaxWatchers:      cfg.MaxWatchers,
		Metrics:          m,
		Logger:           log.With(zap.String("component", "hub")),
	})
	refresher := view.NewRefresher(v, st, view.RefresherOptions{
		Clock:             opts.Clock,
		RefreshInterval:   cfg.RefreshInterval,
		LeaseEvalInterval: cfg.LeaseEvalInterval,
		DiscoveryInterval: cfg.ClusterSetDiscoveryInterval,
		Metrics:           m,
		Logger:            log.With(zap.String("component", "refresher")),
	})
	compactor := view.NewCompactor(v, st, view.CompactorOptions{
		Clock:     opts.Clock,
		Retention: cfg.Retention,
		Interval:  cfg.CompactionInterval,
		LeaseSkew: cfg.LeaseSkew,
		Metrics:   m,
		Logger:    log.With(zap.String("component", "compactor")),
	})
	var endpoints []*gdsv1.ServerEndpoint
	for _, ep := range cfg.AdvertiseEndpoints {
		endpoints = append(endpoints, &gdsv1.ServerEndpoint{Region: ep.Region, GrpcUrl: ep.GRPCURL, HttpUrl: ep.HTTPURL, Primary: ep.Primary})
	}
	apiServer := api.New(api.Options{
		Store:     st,
		View:      v,
		Hub:       hub,
		Refresher: refresher,
		Clock:     opts.Clock,
		Region:    cfg.Region,
		Version:   version.String(),
		Endpoints: endpoints,
		Logger:    log.With(zap.String("component", "api")),
	})
	srv, err := New(Options{
		Config:        cfg,
		Store:         st,
		View:          v,
		Hub:           hub,
		Refresher:     refresher,
		Compactor:     compactor,
		API:           apiServer,
		Metrics:       m,
		Registry:      reg,
		Authenticator: opts.Authenticator,
		Clock:         opts.Clock,
		Logger:        log,
	})
	if err != nil {
		return nil, fmt.Errorf("build server: %w", err)
	}
	return &App{Server: srv, Store: st, View: v, Hub: hub, Metrics: m, Registry: reg}, nil
}

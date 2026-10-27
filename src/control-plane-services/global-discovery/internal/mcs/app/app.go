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

// Package app wires gds-mcs-controller: the identity, the GDS client, the
// cluster lease, the export, auto-export, pool export and import
// controllers, health checks and metrics on one controller-runtime
// manager.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/export"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/identity"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/importer"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/lease"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/version"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/client"
)

// EventSource is the component name of the Events the controller emits.
const EventSource = "gds-mcs-controller"

// Options are the dependencies New does not build itself.
type Options struct {
	// Registry receives the controller and GDS client metrics; nil uses a
	// new registry.
	Registry prometheus.Registerer
	// Logger is the root logger; zero uses ctrl.Log.
	Logger logr.Logger
	// ManagerOptions adjusts the manager options built from the
	// configuration, for example in tests.
	ManagerOptions func(*ctrl.Options)
	// GDSDialOptions are appended to the GDS client's dial options.
	GDSDialOptions []grpc.DialOption
	// AgentID identifies this controller instance in the registration;
	// empty uses the pod name from $HOSTNAME.
	AgentID string
	// Client reads and writes the identity at startup, before the caches
	// run; nil builds a direct client from the REST configuration.
	Client ctrlclient.Client
}

// App is a wired controller, ready to Run.
type App struct {
	Config   config.Config
	Identity identity.Identity
	Manager  manager.Manager
	GDS      *client.Client
	Metrics  *metrics.Metrics
	Lease    *lease.Lease
	Exports  *export.Reconciler
	Resyncer *export.Resyncer
	Importer *importer.Importer

	connected atomic.Bool
}

// New resolves the identity, creates the GDS client and the manager and
// registers every runnable and controller. Nothing talks to GDS before Run.
func New(ctx context.Context, cfg config.Config, restCfg *rest.Config, opts Options) (*App, error) {
	logger := opts.Logger
	if logger.GetSink() == nil {
		logger = ctrl.Log
	}
	reg := opts.Registry
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	scheme, err := config.NewScheme()
	if err != nil {
		return nil, err
	}
	mopts := cfg.ManagerOptions(scheme)
	mopts.Logger = logger
	if opts.ManagerOptions != nil {
		opts.ManagerOptions(&mopts)
	}
	mgr, err := ctrl.NewManager(restCfg, mopts)
	if err != nil {
		return nil, fmt.Errorf("create manager: %w", err)
	}

	direct := opts.Client
	if direct == nil {
		if direct, err = ctrlclient.New(restCfg, ctrlclient.Options{Scheme: scheme}); err != nil {
			return nil, fmt.Errorf("create API client: %w", err)
		}
	}
	id, err := identity.Resolve(ctx, direct, identity.FromConfig(&cfg))
	if err != nil {
		return nil, fmt.Errorf("resolve cluster identity: %w", err)
	}
	if cfg.WriteClusterProperties {
		if err := identity.WriteClusterProperties(ctx, direct, id); err != nil {
			return nil, err
		}
	}
	logger = logger.WithValues("clusterset", id.ClusterSet, "cluster", id.Cluster)

	m := metrics.New()
	if err := m.Register(reg); err != nil {
		return nil, fmt.Errorf("register metrics: %w", err)
	}
	gds, err := client.New(ctx, client.Config{
		URLs:              cfg.GDSURLs,
		CAFile:            cfg.GDSTLSCAFile,
		AllowInsecureHTTP: cfg.GDSAllowInsecureHTTP,
		UserAgent:         "gds-mcs-controller/" + version.Version,
		Registerer:        reg,
		Logger:            slog.New(logr.ToSlogHandler(logger.WithName("gds-client"))),
		DialOptions:       append([]grpc.DialOption{grpc.WithStatsHandler(otelgrpc.NewClientHandler())}, opts.GDSDialOptions...),
	})
	if err != nil {
		return nil, fmt.Errorf("create GDS client: %w", err)
	}

	a := &App{Config: cfg, Identity: id, Manager: mgr, GDS: gds, Metrics: m}
	if err := a.wire(logger, opts); err != nil {
		_ = gds.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) wire(logger logr.Logger, opts Options) error {
	cfg, mgr, id := a.Config, a.Manager, a.Identity
	agentID := opts.AgentID
	if agentID == "" {
		agentID = hostname()
	}
	a.Lease = lease.New(lease.Options{
		GDS:                  a.GDS,
		Identity:             id,
		TTL:                  cfg.LeaseTTL,
		AgentID:              agentID,
		AgentVersion:         version.Version,
		DeregisterOnShutdown: cfg.DeregisterOnShutdown,
		Metrics:              a.Metrics,
		Logger:               logger.WithName("lease"),
		OnRegistered: func(first bool) {
			// A registration after the first means GDS lost or replaced
			// the lease: resend every export.
			if !first {
				a.Resyncer.Trigger(metrics.ResyncReregister)
			}
		},
	})
	a.Exports = export.NewReconciler(export.Options{
		Client:                  mgr.GetClient(),
		GDS:                     a.GDS,
		Identity:                id,
		Lease:                   a.Lease,
		Metrics:                 a.Metrics,
		Recorder:                mgr.GetEventRecorderFor(EventSource),
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
		WatchesNamespace:        cfg.WatchesNamespace,
	})
	a.Resyncer = export.NewResyncer(export.ResyncerOptions{Reconciler: a.Exports, Lease: a.Lease, Logger: logger.WithName("resync")})
	a.GDS.OnReconnect(func(url string) {
		a.Metrics.StreamReconnects.Inc()
		a.Resyncer.Trigger(metrics.ResyncReconnect)
	})
	a.Importer = importer.New(importer.Options{
		Client:                  mgr.GetClient(),
		APIReader:               mgr.GetAPIReader(),
		GDS:                     a.GDS,
		Identity:                id,
		Metrics:                 a.Metrics,
		Logger:                  logger.WithName("importer"),
		EndpointsPerSlice:       cfg.EndpointsPerSlice,
		CreateNamespaces:        cfg.CreateNamespaces,
		AdoptUnmanaged:          cfg.AdoptUnmanagedImports,
		WatchNamespaces:         cfg.WatchNamespaces,
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
		OwnStatus:               a.Exports.SetOwnStatus,
	})
	auto := export.NewAutoExporter(export.AutoExporterOptions{
		Client:                  mgr.GetClient(),
		Identity:                id,
		Metrics:                 a.Metrics,
		MaxConcurrentReconciles: cfg.MaxConcurrentReconciles,
		WatchesNamespace:        cfg.WatchesNamespace,
	})
	for _, r := range []manager.Runnable{a.Lease, a.Resyncer, a.Importer} {
		if err := mgr.Add(r); err != nil {
			return err
		}
	}
	if cfg.AutoExportPools {
		if err := mgr.Add(export.NewPoolExporter(export.PoolExporterOptions{
			Client:           mgr.GetClient(),
			GDS:              a.GDS,
			Identity:         id,
			Metrics:          a.Metrics,
			Logger:           logger.WithName("pools"),
			WatchesNamespace: cfg.WatchesNamespace,
		})); err != nil {
			return err
		}
	}
	if err := a.Exports.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the ServiceExport controller: %w", err)
	}
	if err := auto.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the auto-export controller: %w", err)
	}
	if err := a.Importer.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up the import controller: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	return mgr.AddReadyzCheck("readyz", a.ReadyCheck)
}

// hostname returns the pod name, the agent id of the registration.
func hostname() string {
	if h := os.Getenv("HOSTNAME"); h != "" {
		return h
	}
	h, _ := os.Hostname()
	return h
}

// ReadyCheck is the /readyz check: the GDS client has connected at least
// once and, on the leader, the cluster is registered.
func (a *App) ReadyCheck(*http.Request) error {
	if !a.connected.Load() {
		if a.GDS.CurrentURL() == "" {
			return errors.New("not connected to GDS yet")
		}
		a.connected.Store(true)
	}
	select {
	case <-a.Manager.Elected():
		if !a.Lease.Registered() {
			return errors.New("leader without a registered cluster lease")
		}
	default:
	}
	return nil
}

// Run starts the manager and blocks until ctx is done, then closes the GDS
// client.
func (a *App) Run(ctx context.Context) error {
	defer func() { _ = a.GDS.Close() }()
	ctrl.Log.WithName("setup").Info("Starting gds-mcs-controller", append(version.KeysAndValues(),
		"clusterset", a.Identity.ClusterSet, "cluster", a.Identity.Cluster, "region", a.Identity.Region,
		"zones", a.Identity.Zones, "gdsURLs", a.Config.GDSURLs, "watchNamespaces", a.Config.WatchNamespaces,
		"leaderElect", a.Config.LeaderElect, "autoExportPools", a.Config.AutoExportPools,
		"leaseTTL", a.Config.LeaseTTL.String(), "fieldManager", api.FieldManager)...)
	return a.Manager.Start(ctx)
}

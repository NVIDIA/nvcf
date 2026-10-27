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

package view

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/clock"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// tracerName names the tracer of the view's background loops.
const tracerName = "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"

// RefresherOptions configures a Refresher.
type RefresherOptions struct {
	Clock clock.Clock
	// RefreshInterval is how often each clusterset is reloaded.
	RefreshInterval time.Duration
	// LeaseEvalInterval is how often leases are re-evaluated.
	LeaseEvalInterval time.Duration
	// DiscoveryInterval is how often the store's clustersets are listed.
	DiscoveryInterval time.Duration
	// Parallelism bounds concurrent clusterset loads; default 4.
	Parallelism int
	Metrics     *metrics.Metrics
	Logger      *zap.Logger
}

// Refresher keeps a View in step with the store: it lists clustersets every
// discovery interval, reloads every tracked clusterset every refresh
// interval and re-evaluates leases every lease evaluation interval without
// a store read. Every replica runs one; nothing elects a leader.
type Refresher struct {
	view  *View
	store store.Store
	opts  RefresherOptions
}

// NewRefresher returns a refresher of v from st.
func NewRefresher(v *View, st store.Store, opts RefresherOptions) *Refresher {
	if opts.Clock == nil {
		opts.Clock = v.clock
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 2 * time.Second
	}
	if opts.LeaseEvalInterval <= 0 {
		opts.LeaseEvalInterval = time.Second
	}
	if opts.DiscoveryInterval <= 0 {
		opts.DiscoveryInterval = 30 * time.Second
	}
	if opts.Parallelism <= 0 {
		opts.Parallelism = 4
	}
	return &Refresher{view: v, store: st, opts: opts}
}

// Discover lists the store's clustersets and tracks them.
func (r *Refresher) Discover(ctx context.Context) error {
	names, err := r.store.ListClusterSets(ctx)
	if err != nil {
		r.failed()
		return fmt.Errorf("list clustersets: %w", err)
	}
	r.view.SetDiscovered(names)
	return nil
}

// Refresh reloads one clusterset from the store into the view.
func (r *Refresher) Refresh(ctx context.Context, clusterSet string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gds.view.refresh")
	defer span.End()
	span.SetAttributes(attribute.String("gds.clusterset", clusterSet))

	start := r.opts.Clock.Now()
	snap, err := r.store.LoadClusterSet(ctx, clusterSet)
	if r.opts.Metrics != nil {
		r.opts.Metrics.ViewRefreshDuration.Observe(r.opts.Clock.Now().Sub(start).Seconds())
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		r.failed()
		return fmt.Errorf("load clusterset %q: %w", clusterSet, err)
	}
	snap.ClusterSet = clusterSet
	r.view.Replace(snap, start)
	return nil
}

// RefreshAll reloads every tracked clusterset, Parallelism at a time.
func (r *Refresher) RefreshAll(ctx context.Context) error {
	names := r.view.ClusterSetNames()
	sem := make(chan struct{}, r.opts.Parallelism)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.Refresh(ctx, name); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (r *Refresher) failed() {
	if r.opts.Metrics != nil {
		r.opts.Metrics.ViewRefreshFailures.Inc()
	}
}

// Sync discovers the clustersets and loads every one once.
func (r *Refresher) Sync(ctx context.Context) error {
	if err := r.Discover(ctx); err != nil {
		return err
	}
	return r.RefreshAll(ctx)
}

// Run runs the loops until ctx is done. Failures are logged and retried on
// the next tick; readiness reflects them through the view age.
func (r *Refresher) Run(ctx context.Context) {
	log := r.opts.Logger
	if err := r.Sync(ctx); err != nil && ctx.Err() == nil {
		log.Warn("initial view load failed; retrying", zap.Error(err))
	}
	refresh := r.opts.Clock.NewTicker(r.opts.RefreshInterval)
	defer refresh.Stop()
	lease := r.opts.Clock.NewTicker(r.opts.LeaseEvalInterval)
	defer lease.Stop()
	discover := r.opts.Clock.NewTicker(r.opts.DiscoveryInterval)
	defer discover.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-discover.C():
			if err := r.Discover(ctx); err != nil && ctx.Err() == nil {
				log.Warn("clusterset discovery failed", zap.Error(err))
			}
		case <-refresh.C():
			// Until the first discovery succeeds, retry it every refresh
			// interval rather than every discovery interval.
			if !r.view.Discovered() {
				if err := r.Discover(ctx); err != nil && ctx.Err() == nil {
					log.Warn("clusterset discovery failed", zap.Error(err))
				}
			}
			if err := r.RefreshAll(ctx); err != nil && ctx.Err() == nil {
				log.Warn("view refresh failed", zap.Error(err))
			}
		case <-r.view.Kicked():
			if err := r.RefreshAll(ctx); err != nil && ctx.Err() == nil {
				log.Warn("view refresh failed", zap.Error(err))
			}
		case <-lease.C():
			r.view.EvaluateLeases()
		}
	}
}

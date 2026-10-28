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
	"time"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/clock"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// CompactorOptions configures a Compactor.
type CompactorOptions struct {
	Clock clock.Clock
	// Retention keeps the records of a dead cluster or pool this long after
	// its lease expired, so a returning writer keeps its export creation
	// timestamps.
	Retention time.Duration
	// Interval is how often compaction runs.
	Interval time.Duration
	// LeaseSkew is the clock skew allowance of lease evaluation.
	LeaseSkew time.Duration
	Metrics   *metrics.Metrics
	Logger    *zap.Logger
}

// Compactor removes records past retention from the store and the view:
//
//   - a cluster whose lease expired more than Retention ago, with
//     everything it owns (store.DeleteCluster);
//   - exports and pools of an unregistered cluster, and endpoint sets
//     without an export, not updated for Retention;
//   - pool agent registrations whose lease expired more than Retention
//     ago, and registrations without a lease not updated for Retention;
//   - API pools whose lease expired more than Retention ago, and API pool
//     records without a lease (overlays of label-derived pools) whose
//     labelled exports are gone and that were not updated for Retention.
//     A pool record is kept while an agent registration of the pool
//     remains, since deleting the pool would remove the registrations.
//
// Every replica compacts. Deletes are idempotent, so concurrent compactors
// agree. The store has no conditional delete; a cluster is re-read just
// before its deletion to narrow the race with a returning controller, which
// recovers anyway by registering again on NOT_FOUND and resending its
// exports.
type Compactor struct {
	view  *View
	store store.Store
	opts  CompactorOptions
}

// NewCompactor returns a compactor of v and st.
func NewCompactor(v *View, st store.Store, opts CompactorOptions) *Compactor {
	if opts.Clock == nil {
		opts.Clock = v.clock
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Retention <= 0 {
		opts.Retention = 24 * time.Hour
	}
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Minute
	}
	return &Compactor{view: v, store: st, opts: opts}
}

// Run compacts every Interval until ctx is done.
func (c *Compactor) Run(ctx context.Context) {
	t := c.opts.Clock.NewTicker(c.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			if err := c.Compact(ctx); err != nil && ctx.Err() == nil {
				c.opts.Logger.Warn("compaction failed", zap.Error(err))
			}
		}
	}
}

func (c *Compactor) deleted(kind string, n int) {
	if c.opts.Metrics != nil && n > 0 {
		c.opts.Metrics.CompactionDeleted.WithLabelValues(kind).Add(float64(n))
	}
}

// dead reports whether a lease expired more than the retention ago.
func (c *Compactor) dead(l store.Lease, now time.Time) bool {
	return now.After(l.ExpiresAt().Add(c.opts.LeaseSkew).Add(c.opts.Retention))
}

// Compact runs one compaction pass over every tracked clusterset.
func (c *Compactor) Compact(ctx context.Context) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gds.view.compact")
	defer span.End()
	var errs []error
	for _, name := range c.view.ClusterSetNames() {
		if err := c.compactClusterSet(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Compactor) compactClusterSet(ctx context.Context, cs string) error {
	now := c.opts.Clock.Now()
	snap := c.view.Records(cs)
	log := c.opts.Logger.With(zap.String("clusterset", cs))
	var errs []error

	registered := map[string]bool{}
	gone := map[string]bool{}
	for _, r := range snap.Clusters {
		registered[r.Key.Cluster] = true
		if !c.dead(r.Lease, now) {
			continue
		}
		current, err := c.store.GetCluster(ctx, r.Key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			c.view.ApplyClusterDelete(r.Key)
			delete(registered, r.Key.Cluster)
			gone[r.Key.Cluster] = true
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("get cluster %s: %w", r.Key.Cluster, err))
			continue
		case !c.dead(current.Lease, now):
			c.view.ApplyCluster(current)
			continue
		}
		if err := c.store.DeleteCluster(ctx, r.Key); err != nil {
			errs = append(errs, fmt.Errorf("delete cluster %s: %w", r.Key.Cluster, err))
			continue
		}
		c.view.ApplyClusterDelete(r.Key)
		delete(registered, r.Key.Cluster)
		gone[r.Key.Cluster] = true
		c.deleted(metrics.RecordCluster, 1)
		log.Info("compacted expired cluster", zap.String("cluster", r.Key.Cluster),
			zap.Time("lease_expired_at", r.Lease.ExpiresAt()))
	}

	old := func(t time.Time) bool { return now.After(t.Add(c.opts.Retention)) }
	exports := map[store.ExportKey]bool{}
	labelled := map[poolIdent]bool{}
	for _, r := range snap.Exports {
		if gone[r.Key.Cluster] {
			continue
		}
		exports[r.Key] = true
		if len(poolRoles(r.Spec.Labels, r.Spec.Annotations)) > 0 {
			labelled[poolIdent{cluster: r.Key.Cluster, name: r.Spec.Labels[api.LabelPool]}] = true
		}
		if registered[r.Key.Cluster] || !old(r.UpdatedAt) {
			continue
		}
		if err := c.store.DeleteExport(ctx, r.Key); err != nil {
			errs = append(errs, fmt.Errorf("delete export %s/%s/%s: %w", r.Key.Cluster, r.Key.Namespace, r.Key.Name, err))
			continue
		}
		c.view.ApplyExportDelete(r.Key)
		delete(exports, r.Key)
		c.deleted(metrics.RecordExport, 1)
	}
	for _, r := range snap.EndpointSets {
		if gone[r.Key.Cluster] || exports[r.Key] || !old(r.UpdatedAt) {
			continue
		}
		if err := c.store.DeleteEndpointSet(ctx, r.Key); err != nil {
			errs = append(errs, fmt.Errorf("delete endpoint set %s/%s/%s: %w", r.Key.Cluster, r.Key.Namespace, r.Key.Name, err))
			continue
		}
		c.view.ApplyEndpointSetDelete(r.Key)
		c.deleted(metrics.RecordEndpointSet, 1)
	}
	// Agent registrations go first, so that a pool record outlives them.
	withAgents := map[poolIdent]bool{}
	for _, r := range snap.PoolAgents {
		if gone[r.Key.Cluster] {
			continue
		}
		stale := c.dead(r.Lease, now)
		if r.Lease.Epoch == 0 {
			stale = old(r.UpdatedAt)
		}
		if !stale {
			withAgents[poolIdent{cluster: r.Key.Cluster, name: r.PoolName}] = true
			continue
		}
		if err := c.store.DeletePoolAgent(ctx, r.Key); err != nil {
			errs = append(errs, fmt.Errorf("delete pool agent %s/%s/%s: %w", r.Key.PoolID, r.Key.Role, r.Key.AgentID, err))
			withAgents[poolIdent{cluster: r.Key.Cluster, name: r.PoolName}] = true
			continue
		}
		c.view.ApplyPoolAgentDelete(r.Key)
		c.deleted(metrics.RecordPoolAgent, 1)
		log.Info("compacted stale pool agent registration", zap.String("cluster", r.Key.Cluster), zap.String("pool_id", r.Key.PoolID),
			zap.String("role", string(r.Key.Role)), zap.String("agent_id", r.Key.AgentID))
	}
	for _, r := range snap.Pools {
		if gone[r.Key.Cluster] || withAgents[poolIdent{cluster: r.Key.Cluster, name: r.Name}] {
			continue
		}
		var stale bool
		switch {
		case r.Lease.Epoch != 0:
			stale = c.dead(r.Lease, now)
		case !registered[r.Key.Cluster]:
			stale = old(r.UpdatedAt)
		default:
			stale = !labelled[poolIdent{cluster: r.Key.Cluster, name: r.Name}] && len(r.Spec.Services) == 0 && old(r.UpdatedAt)
		}
		if !stale {
			continue
		}
		if err := c.store.DeletePool(ctx, r.Key); err != nil {
			errs = append(errs, fmt.Errorf("delete pool %s: %w", r.Key.PoolID, err))
			continue
		}
		c.view.ApplyPoolDelete(r.Key)
		c.deleted(metrics.RecordPool, 1)
		log.Info("compacted stale pool", zap.String("cluster", r.Key.Cluster), zap.String("pool_id", r.Key.PoolID))
	}
	return errors.Join(errs...)
}

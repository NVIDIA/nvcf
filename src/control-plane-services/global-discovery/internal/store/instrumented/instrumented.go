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

// Package instrumented wraps a store.Store with the nvcf_gds_store_*
// metrics and an OpenTelemetry child span per call (gds.store.<operation>).
package instrumented

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

const tracerName = "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"

// Store is an instrumented store.Store.
type Store struct {
	next    store.Store
	metrics *metrics.Metrics
	tracer  trace.Tracer
}

var _ store.Store = (*Store)(nil)

// New wraps next. m may be nil to record spans only.
func New(next store.Store, m *metrics.Metrics) *Store {
	return &Store{next: next, metrics: m, tracer: otel.Tracer(tracerName)}
}

// Unwrap returns the wrapped store.
func (s *Store) Unwrap() store.Store { return s.next }

// conditionalReason classifies an error a write's condition produced.
func conditionalReason(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrStaleGeneration{}):
		return "stale_generation", true
	case errors.Is(err, store.ErrLeaseEpochMismatch):
		return "lease_epoch_mismatch", true
	case errors.Is(err, store.ErrPoolNameTaken):
		return "pool_name_taken", true
	case errors.Is(err, store.ErrTooLarge):
		return "too_large", true
	case errors.Is(err, store.ErrNotFound):
		return "not_found", true
	}
	return "", false
}

func (s *Store) start(ctx context.Context, op string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	ctx, span := s.tracer.Start(ctx, "gds.store."+op, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	begin := time.Now()
	return ctx, func(err error) {
		result := "ok"
		if err != nil {
			reason, conditional := conditionalReason(err)
			switch {
			case conditional && op == "get_cluster":
				// A miss is an answer, not a failure.
			case conditional:
				result = "conditional"
				span.SetAttributes(attribute.String("gds.store.condition", reason))
				if s.metrics != nil {
					s.metrics.StoreConditionalFailures.WithLabelValues(op, reason).Inc()
				}
			default:
				result = "error"
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				span.SetAttributes(attribute.Bool("error", true))
			}
		}
		if s.metrics != nil {
			s.metrics.StoreRequestDuration.WithLabelValues(op, result).Observe(time.Since(begin).Seconds())
		}
		span.End()
	}
}

func clusterAttrs(k store.ClusterKey) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String("gds.clusterset", k.ClusterSet), attribute.String("nvcf.cluster.id", k.Cluster)}
}

func exportAttrs(k store.ExportKey) []attribute.KeyValue {
	return append(clusterAttrs(k.ClusterKey()), attribute.String("k8s.namespace.name", k.Namespace), attribute.String("k8s.service.name", k.Name))
}

func poolAttrs(k store.PoolKey) []attribute.KeyValue {
	return append(clusterAttrs(k.ClusterKey()), attribute.String("gds.pool.id", k.PoolID))
}

// PutCluster implements store.Store.
func (s *Store) PutCluster(ctx context.Context, rec store.ClusterRecord, now time.Time) (out store.ClusterRecord, err error) {
	ctx, done := s.start(ctx, "put_cluster", clusterAttrs(rec.Key)...)
	defer func() { done(err) }()
	return s.next.PutCluster(ctx, rec, now)
}

// RenewClusterLease implements store.Store.
func (s *Store) RenewClusterLease(ctx context.Context, key store.ClusterKey, epoch uint64, now time.Time) (out store.ClusterRecord, err error) {
	ctx, done := s.start(ctx, "renew_cluster_lease", clusterAttrs(key)...)
	defer func() { done(err) }()
	return s.next.RenewClusterLease(ctx, key, epoch, now)
}

// GetCluster implements store.Store.
func (s *Store) GetCluster(ctx context.Context, key store.ClusterKey) (out store.ClusterRecord, err error) {
	ctx, done := s.start(ctx, "get_cluster", clusterAttrs(key)...)
	defer func() { done(err) }()
	return s.next.GetCluster(ctx, key)
}

// DeleteCluster implements store.Store.
func (s *Store) DeleteCluster(ctx context.Context, key store.ClusterKey) (err error) {
	ctx, done := s.start(ctx, "delete_cluster", clusterAttrs(key)...)
	defer func() { done(err) }()
	return s.next.DeleteCluster(ctx, key)
}

// PutExport implements store.Store.
func (s *Store) PutExport(ctx context.Context, rec store.ExportRecord) (err error) {
	ctx, done := s.start(ctx, "put_export", exportAttrs(rec.Key)...)
	defer func() { done(err) }()
	return s.next.PutExport(ctx, rec)
}

// DeleteExport implements store.Store.
func (s *Store) DeleteExport(ctx context.Context, key store.ExportKey) (err error) {
	ctx, done := s.start(ctx, "delete_export", exportAttrs(key)...)
	defer func() { done(err) }()
	return s.next.DeleteExport(ctx, key)
}

// PutEndpointSet implements store.Store.
func (s *Store) PutEndpointSet(ctx context.Context, rec store.EndpointSetRecord) (err error) {
	ctx, done := s.start(ctx, "put_endpoint_set", exportAttrs(rec.Key)...)
	defer func() { done(err) }()
	return s.next.PutEndpointSet(ctx, rec)
}

// DeleteEndpointSet implements store.Store.
func (s *Store) DeleteEndpointSet(ctx context.Context, key store.ExportKey) (err error) {
	ctx, done := s.start(ctx, "delete_endpoint_set", exportAttrs(key)...)
	defer func() { done(err) }()
	return s.next.DeleteEndpointSet(ctx, key)
}

// PutPool implements store.Store.
func (s *Store) PutPool(ctx context.Context, rec store.PoolRecord, opts store.PutPoolOptions) (out store.PoolRecord, err error) {
	ctx, done := s.start(ctx, "put_pool", poolAttrs(rec.Key)...)
	defer func() { done(err) }()
	return s.next.PutPool(ctx, rec, opts)
}

// RenewPoolLease implements store.Store.
func (s *Store) RenewPoolLease(ctx context.Context, key store.PoolKey, epoch uint64, now time.Time, patch store.PoolHeartbeatPatch) (out store.PoolRecord, err error) {
	ctx, done := s.start(ctx, "renew_pool_lease", poolAttrs(key)...)
	defer func() { done(err) }()
	return s.next.RenewPoolLease(ctx, key, epoch, now, patch)
}

// DeletePool implements store.Store.
func (s *Store) DeletePool(ctx context.Context, key store.PoolKey) (err error) {
	ctx, done := s.start(ctx, "delete_pool", poolAttrs(key)...)
	defer func() { done(err) }()
	return s.next.DeletePool(ctx, key)
}

func poolAgentAttrs(k store.PoolAgentKey) []attribute.KeyValue {
	return append(poolAttrs(k.PoolKey()), attribute.String("gds.pool_agent.role", string(k.Role)), attribute.String("gds.pool_agent.id", k.AgentID))
}

// PutPoolAgent implements store.Store.
func (s *Store) PutPoolAgent(ctx context.Context, rec store.PoolAgentRecord, opts store.PutPoolAgentOptions) (out store.PoolAgentRecord, err error) {
	ctx, done := s.start(ctx, "put_pool_agent", poolAgentAttrs(rec.Key)...)
	defer func() { done(err) }()
	return s.next.PutPoolAgent(ctx, rec, opts)
}

// RenewPoolAgentLease implements store.Store.
func (s *Store) RenewPoolAgentLease(ctx context.Context, key store.PoolAgentKey, epoch uint64, now time.Time, patch store.PoolAgentHeartbeatPatch) (out store.PoolAgentRecord, err error) {
	ctx, done := s.start(ctx, "renew_pool_agent_lease", poolAgentAttrs(key)...)
	defer func() { done(err) }()
	return s.next.RenewPoolAgentLease(ctx, key, epoch, now, patch)
}

// DeletePoolAgent implements store.Store.
func (s *Store) DeletePoolAgent(ctx context.Context, key store.PoolAgentKey) (err error) {
	ctx, done := s.start(ctx, "delete_pool_agent", poolAgentAttrs(key)...)
	defer func() { done(err) }()
	return s.next.DeletePoolAgent(ctx, key)
}

// ListClusterSets implements store.Store.
func (s *Store) ListClusterSets(ctx context.Context) (out []string, err error) {
	ctx, done := s.start(ctx, "list_cluster_sets")
	defer func() { done(err) }()
	return s.next.ListClusterSets(ctx)
}

// LoadClusterSet implements store.Store.
func (s *Store) LoadClusterSet(ctx context.Context, clusterSet string) (out store.ClusterSetSnapshot, err error) {
	ctx, done := s.start(ctx, "load_cluster_set", attribute.String("gds.clusterset", clusterSet))
	defer func() { done(err) }()
	return s.next.LoadClusterSet(ctx, clusterSet)
}

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) (err error) {
	ctx, done := s.start(ctx, "ping")
	defer func() { done(err) }()
	return s.next.Ping(ctx)
}

// Close implements store.Store.
func (s *Store) Close() error { return s.next.Close() }

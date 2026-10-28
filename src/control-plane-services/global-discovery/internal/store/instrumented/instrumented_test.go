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

package instrumented

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/memory"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return New(memory.New(), nil) })
}

func TestMetricsAndSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	m := metrics.New(prometheus.NewRegistry())
	st := New(memory.New(), m)
	assert.NotNil(t, st.Unwrap())
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	key := store.ClusterKey{ClusterSet: "cs", Cluster: "a"}

	_, err := st.GetCluster(ctx, key)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PutCluster(ctx, store.ClusterRecord{Key: key, Lease: store.Lease{TTL: time.Minute}}, now)
	require.NoError(t, err)
	_, err = st.RenewClusterLease(ctx, key, 1, now)
	require.ErrorIs(t, err, store.ErrLeaseEpochMismatch)
	ek := store.ExportKey{ClusterSet: "cs", Cluster: "a", Namespace: "ns", Name: "svc"}
	require.NoError(t, st.PutExport(ctx, store.ExportRecord{Key: ek, Generation: 2}))
	require.ErrorIs(t, st.PutExport(ctx, store.ExportRecord{Key: ek, Generation: 1}), store.ErrStaleGeneration{})
	require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek, Generation: 1}))
	require.ErrorIs(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek, Generation: 2, Endpoints: make([]store.Endpoint, 1001)}), store.ErrTooLarge)
	require.NoError(t, st.DeleteEndpointSet(ctx, ek))
	require.NoError(t, st.DeleteExport(ctx, ek))
	pk := store.PoolKey{ClusterSet: "cs", Cluster: "a", PoolID: "p"}
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk, Name: "p", Generation: 1}, store.PutPoolOptions{Now: now})
	require.NoError(t, err)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: store.PoolKey{ClusterSet: "cs", Cluster: "a", PoolID: "q"}, Name: "p", Generation: 1}, store.PutPoolOptions{Now: now})
	require.ErrorIs(t, err, store.ErrPoolNameTaken)
	_, err = st.RenewPoolLease(ctx, pk, 1, now, store.PoolHeartbeatPatch{})
	require.ErrorIs(t, err, store.ErrLeaseEpochMismatch)
	require.NoError(t, st.DeletePool(ctx, pk))
	ak := store.PoolAgentKey{ClusterSet: "cs", Cluster: "a", PoolID: "p", Role: store.FrontendRolePylon, AgentID: "pod-0"}
	_, err = st.PutPoolAgent(ctx, store.PoolAgentRecord{Key: ak, Generation: 2}, store.PutPoolAgentOptions{Now: now, MintLease: true})
	require.NoError(t, err)
	_, err = st.PutPoolAgent(ctx, store.PoolAgentRecord{Key: ak, Generation: 1}, store.PutPoolAgentOptions{Now: now})
	require.ErrorIs(t, err, store.ErrStaleGeneration{})
	_, err = st.RenewPoolAgentLease(ctx, ak, 1, now, store.PoolAgentHeartbeatPatch{})
	require.ErrorIs(t, err, store.ErrLeaseEpochMismatch)
	require.NoError(t, st.DeletePoolAgent(ctx, ak))
	_, err = st.ListClusterSets(ctx)
	require.NoError(t, err)
	_, err = st.LoadClusterSet(ctx, "cs")
	require.NoError(t, err)
	require.NoError(t, st.Ping(ctx))
	require.NoError(t, st.DeleteCluster(ctx, key))
	require.NoError(t, st.Close())
	require.ErrorIs(t, st.Ping(ctx), store.ErrUnavailable)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("put_export", "stale_generation")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("put_endpoint_set", "too_large")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("put_pool", "pool_name_taken")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("renew_cluster_lease", "lease_epoch_mismatch")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("put_pool_agent", "stale_generation")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StoreConditionalFailures.WithLabelValues("renew_pool_agent_lease", "lease_epoch_mismatch")))
	assert.Equal(t, 1, testutil.CollectAndCount(m.StoreRequestDuration.WithLabelValues("delete_pool_agent", "ok").(prometheus.Histogram)))

	spans := rec.Ended()
	require.NotEmpty(t, spans)
	assert.Equal(t, "gds.store.get_cluster", spans[0].Name())
	last := spans[len(spans)-1]
	assert.Equal(t, "gds.store.ping", last.Name())
	assert.Equal(t, "Error", last.Status().Code.String())
}

func TestConditionalReason(t *testing.T) {
	_, ok := conditionalReason(errors.New("x"))
	assert.False(t, ok)
	r, ok := conditionalReason(store.ErrNotFound)
	assert.True(t, ok)
	assert.Equal(t, "not_found", r)
}

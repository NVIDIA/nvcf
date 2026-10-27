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

package export

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	mcsv1beta1 "sigs.k8s.io/mcs-api/pkg/apis/v1beta1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/mcstest"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/client"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/pkg/fakegds"
)

func autoReconcile(t *testing.T, a *AutoExporter, name string) {
	t.Helper()
	_, err := a.Reconcile(mcstest.Ctx(t), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
}

func getExport(t *testing.T, c ctrlclient.Client, name string) (*mcsv1beta1.ServiceExport, bool) {
	t.Helper()
	var se mcsv1beta1.ServiceExport
	err := c.Get(mcstest.Ctx(t), types.NamespacedName{Namespace: ns, Name: name}, &se)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return &se, true
}

func TestAutoExportFollowsTheLabel(t *testing.T) {
	svc := mcstest.Service(ns, "web", map[string]string{api.LabelExport: api.ExportEnabled})
	c := mcstest.FakeClient(t, svc)
	a := NewAutoExporter(AutoExporterOptions{Client: c, Identity: mcstest.Identity()})

	autoReconcile(t, a, "web")
	se, ok := getExport(t, c, "web")
	require.True(t, ok)
	assert.True(t, Managed(se))
	assert.Equal(t, mcstest.ClusterSet, se.Labels[api.LabelClusterSet])
	require.Len(t, se.OwnerReferences, 1)
	assert.Equal(t, svc.UID, se.OwnerReferences[0].UID)
	assert.Equal(t, "Service", se.OwnerReferences[0].Kind)

	autoReconcile(t, a, "web") // idempotent
	svc.Labels = nil
	require.NoError(t, c.Update(mcstest.Ctx(t), svc))
	autoReconcile(t, a, "web")
	_, ok = getExport(t, c, "web")
	assert.False(t, ok, "deleted when the label goes")

	// A Service that disappears takes its export along.
	svc.Labels = map[string]string{api.LabelExport: api.ExportEnabled}
	require.NoError(t, c.Update(mcstest.Ctx(t), svc))
	autoReconcile(t, a, "web")
	require.NoError(t, c.Delete(mcstest.Ctx(t), svc))
	autoReconcile(t, a, "web")
	_, ok = getExport(t, c, "web")
	assert.False(t, ok)
}

func TestAutoExportNeverTouchesForeignExports(t *testing.T) {
	explicit := mcstest.ServiceExport(ns, "web")
	c := mcstest.FakeClient(t, mcstest.Service(ns, "web", nil), explicit)
	a := NewAutoExporter(AutoExporterOptions{Client: c, Identity: mcstest.Identity()})
	autoReconcile(t, a, "web")
	_, ok := getExport(t, c, "web")
	assert.True(t, ok, "an unlabelled Service keeps a ServiceExport it did not get from the controller")

	svc := mcstest.Service(ns, "api", map[string]string{api.LabelExport: api.ExportEnabled})
	require.NoError(t, c.Create(mcstest.Ctx(t), svc))
	require.NoError(t, c.Create(mcstest.Ctx(t), mcstest.ServiceExport(ns, "api")))
	autoReconcile(t, a, "api")
	se, _ := getExport(t, c, "api")
	assert.False(t, Managed(se))
	assert.Empty(t, se.OwnerReferences)
}

func TestAutoExportAndPoolExportsShareOneObject(t *testing.T) {
	svc := mcstest.Service(ns, "web", map[string]string{api.LabelExport: api.ExportEnabled})
	pool := mcstest.ServiceExport(ns, "web")
	pool.Labels = map[string]string{api.LabelManagedBy: api.ManagedByController, api.LabelPoolID: "p1"}
	c := mcstest.FakeClient(t, svc, pool)
	a := NewAutoExporter(AutoExporterOptions{Client: c, Identity: mcstest.Identity()})

	autoReconcile(t, a, "web")
	se, _ := getExport(t, c, "web")
	require.Len(t, se.OwnerReferences, 1, "the labelled Service owns it too")

	svc.Labels = nil
	require.NoError(t, c.Update(mcstest.Ctx(t), svc))
	autoReconcile(t, a, "web")
	se, ok := getExport(t, c, "web")
	require.True(t, ok, "kept for the pool")
	assert.Empty(t, se.OwnerReferences)
	autoReconcile(t, a, "web")

	require.NoError(t, c.Delete(mcstest.Ctx(t), svc))
	autoReconcile(t, a, "web")
	_, ok = getExport(t, c, "web")
	assert.True(t, ok)

	a.opts.WatchesNamespace = func(string) bool { return false }
	autoReconcile(t, a, "web")
}

// poolEnv runs the pool half against fakegds.
type poolEnv struct {
	gds *mcstest.GDS
	k8s ctrlclient.WithWatch
	p   *PoolExporter
	reg *prometheus.Registry
}

func newPoolEnv(t *testing.T, objs ...ctrlclient.Object) *poolEnv {
	t.Helper()
	e := &poolEnv{gds: mcstest.StartGDS(t), k8s: mcstest.FakeClient(t, objs...), reg: prometheus.NewRegistry()}
	e.gds.Server.EnsureCluster(mcstest.ClusterSet, mcstest.Cluster, mcstest.Region)
	m := metrics.New()
	require.NoError(t, m.Register(e.reg))
	e.p = NewPoolExporter(PoolExporterOptions{Client: e.k8s, GDS: e.gds.Client, Identity: mcstest.Identity(), Metrics: m,
		Logger: testr.New(t), Interval: 50 * time.Millisecond})
	return e
}

func (e *poolEnv) register(t *testing.T, cluster, name string, services ...string) string {
	t.Helper()
	pool := &gdsv1.Pool{Name: name, Location: &gdsv1.Location{Cluster: cluster}}
	for _, s := range services {
		pool.Services = append(pool.Services, &gdsv1.ServiceRef{Namespace: ns, Name: s, Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI})
	}
	resp, err := e.gds.Client.Pools().Register(mcstest.Ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: mcstest.ClusterSet, Pool: pool, LeaseTtl: durationpb.New(time.Minute),
	})
	require.NoError(t, err)
	return resp.GetPool().GetPoolId()
}

func (e *poolEnv) snapshot(t *testing.T) *client.PoolState {
	t.Helper()
	resp, err := e.gds.Client.Pools().List(mcstest.Ctx(t), &gdsv1.ListPoolsRequest{Clusterset: mcstest.ClusterSet, Cluster: mcstest.Cluster})
	require.NoError(t, err)
	return &client.PoolState{Pools: resp.GetPools(), Synced: true}
}

func TestPoolExportsFollowAPIRegisteredPools(t *testing.T) {
	labelled := mcstest.Service(ns, "labelled", map[string]string{api.LabelExport: api.ExportEnabled})
	foreign := mcstest.ServiceExport(ns, "foreign")
	e := newPoolEnv(t, labelled, foreign)
	ctx := mcstest.Ctx(t)

	require.NoError(t, e.p.Sync(ctx, nil))
	require.NoError(t, e.p.Sync(ctx, &client.PoolState{}), "an unsynced snapshot changes nothing")

	id := e.register(t, mcstest.Cluster, "p1", "frontend", "labelled", "foreign")
	e.register(t, "b", "elsewhere", "remote")
	// A label-only pool has no lease and is skipped.
	e.gds.Server.PutExport(mcstest.ClusterSet, mcstest.Cluster, ns, "labelonly", fakegds.PoolFrontendSpec("p2", 8000), nil)

	require.NoError(t, e.p.Sync(ctx, e.snapshot(t)))
	se, ok := getExport(t, e.k8s, "frontend")
	require.True(t, ok)
	assert.Equal(t, id, se.Labels[api.LabelPoolID])
	assert.True(t, Managed(se))
	_, ok = getExport(t, e.k8s, "remote")
	assert.False(t, ok, "another cluster's pool")
	_, ok = getExport(t, e.k8s, "labelonly")
	assert.False(t, ok, "label-only pools need no export")
	se, _ = getExport(t, e.k8s, "foreign")
	assert.Empty(t, se.Labels, "foreign exports are not touched")
	assert.Equal(t, 2.0, mcstest.MetricValue(t, e.reg, "nvcf_gds_mcs_controller_pool_exports", nil))

	// The labelled Service's export, created for the pool first, gets the
	// Service as owner from the label half and keeps the pool label.
	se, _ = getExport(t, e.k8s, "labelled")
	assert.Equal(t, id, se.Labels[api.LabelPoolID])
	auto := NewAutoExporter(AutoExporterOptions{Client: e.k8s, Identity: mcstest.Identity()})
	_, err := auto.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "labelled"}})
	require.NoError(t, err)
	se, _ = getExport(t, e.k8s, "labelled")
	assert.Len(t, se.OwnerReferences, 1)
	assert.Equal(t, id, se.Labels[api.LabelPoolID])

	// Another pool id is relabelled.
	se.Labels[api.LabelPoolID] = "older"
	require.NoError(t, e.k8s.Update(ctx, se))
	require.NoError(t, e.p.Sync(ctx, e.snapshot(t)))
	se, _ = getExport(t, e.k8s, "labelled")
	assert.Equal(t, id, se.Labels[api.LabelPoolID])

	// DRAINING keeps the exports.
	_, err = e.gds.Client.Pools().Update(ctx, &gdsv1.UpdatePoolRequest{Clusterset: mcstest.ClusterSet, PoolId: id,
		Pool: &gdsv1.Pool{DesiredState: gdsv1.PoolState_POOL_STATE_DRAINING}, UpdateMask: mask("desired_state"),
		Generation: client.NextGeneration(0)})
	require.NoError(t, err)
	require.NoError(t, e.p.Sync(ctx, e.snapshot(t)))
	_, ok = getExport(t, e.k8s, "frontend")
	assert.True(t, ok)

	// A stale snapshot without the pool removes nothing.
	require.NoError(t, e.p.Sync(ctx, &client.PoolState{Synced: true, Stale: true}))
	_, ok = getExport(t, e.k8s, "frontend")
	assert.True(t, ok)

	// GONE: the pool's exports go; the labelled Service keeps its export.
	_, err = e.gds.Client.Pools().Deregister(ctx, &gdsv1.DeregisterPoolRequest{Clusterset: mcstest.ClusterSet, PoolId: id})
	require.NoError(t, err)
	require.NoError(t, e.p.Sync(ctx, e.snapshot(t)))
	_, ok = getExport(t, e.k8s, "frontend")
	assert.False(t, ok)
	se, ok = getExport(t, e.k8s, "labelled")
	require.True(t, ok)
	assert.Empty(t, se.Labels[api.LabelPoolID])
	_, ok = getExport(t, e.k8s, "foreign")
	assert.True(t, ok)
	assert.Equal(t, 0.0, mcstest.MetricValue(t, e.reg, "nvcf_gds_mcs_controller_pool_exports", nil))
}

func TestPoolExportsOfAGonePoolInAState(t *testing.T) {
	e := newPoolEnv(t)
	id := e.register(t, mcstest.Cluster, "p1", "frontend")
	st := e.snapshot(t)
	require.NoError(t, e.p.Sync(mcstest.Ctx(t), st))
	_, ok := getExport(t, e.k8s, "frontend")
	require.True(t, ok)
	st.Pools[0].State = gdsv1.PoolState_POOL_STATE_GONE
	require.NoError(t, e.p.Sync(mcstest.Ctx(t), st))
	_, ok = getExport(t, e.k8s, "frontend")
	assert.False(t, ok, "a GONE tombstone removes the exports of %s", id)
}

func TestPoolExporterRuns(t *testing.T) {
	e := newPoolEnv(t)
	assert.True(t, e.p.NeedLeaderElection())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.p.Start(ctx) }()
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()
	e.register(t, mcstest.Cluster, "p1", "frontend")
	require.Eventually(t, func() bool {
		_, ok := getExport(t, e.k8s, "frontend")
		return ok
	}, 5*time.Second, 10*time.Millisecond)
	// A ServiceExport deleted by hand comes back on the next sync.
	se, _ := getExport(t, e.k8s, "frontend")
	require.NoError(t, e.k8s.Delete(mcstest.Ctx(t), se))
	require.Eventually(t, func() bool {
		_, ok := getExport(t, e.k8s, "frontend")
		return ok
	}, 5*time.Second, 10*time.Millisecond)
}

func TestPoolExportErrors(t *testing.T) {
	e := newPoolEnv(t)
	e.register(t, mcstest.Cluster, "p1", "frontend")
	st := e.snapshot(t)
	e.p.opts.WatchesNamespace = func(string) bool { return false }
	require.NoError(t, e.p.Sync(mcstest.Ctx(t), st))
	_, ok := getExport(t, e.k8s, "frontend")
	assert.False(t, ok, "namespace not watched")
}

func mask(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }

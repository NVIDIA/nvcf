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

package api

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

func apiPool(name, cluster string) *gdsv1.Pool {
	return &gdsv1.Pool{
		Name:          name,
		Location:      &gdsv1.Location{Cluster: cluster, Region: "us-east-1", Dc: "dc1"},
		HardwareClass: "h100",
		Models:        []*gdsv1.ModelServed{{ModelId: "mistral", Aliases: []string{"mistral-7b"}}},
		Frontends:     []*gdsv1.FrontendEndpoint{{Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, Url: "https://" + name + ".example"}},
		Labels:        map[string]string{"tier": "gold"},
		OwnerId:       "relay-0",
	}
}

func TestPoolFlow(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	reg, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("mistral", "a"), Generation: 1})
	require.NoError(t, err)
	p := reg.GetPool()
	id := p.GetPoolId()
	assert.Equal(t, registry.PoolID(cs, "a", "mistral"), id)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, p.GetState())
	assert.Equal(t, 60*time.Second, p.GetLease().GetTtl().AsDuration())
	epoch := p.GetLease().GetEpoch()

	// The first heartbeat makes it ACTIVE and reports worker counts.
	e.clock.Advance(20 * time.Second)
	workers, ready := int32(8), int32(6)
	hb, err := e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{
		Clusterset: cs, PoolId: id, Epoch: epoch, WorkerCount: &workers, ReadyWorkerCount: &ready,
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, hb.GetState())
	got, err := e.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	assert.Equal(t, int32(8), got.GetPool().GetWorkerCount())

	_, err = e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{Clusterset: cs, PoolId: id, Epoch: epoch + 1})
	requireError(t, err, codes.Aborted, gds.ReasonLeaseEpochMismatch)
	_, err = e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{Clusterset: cs, PoolId: "unknown", Epoch: 1})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)

	// Re-registration is idempotent on (cluster, name): same id, new epoch.
	e.clock.Advance(time.Second)
	again, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{
		Clusterset: cs, Pool: apiPool("mistral", "a"), LeaseTtl: durationpb.New(10 * time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, id, again.GetPool().GetPoolId())
	assert.Greater(t, again.GetPool().GetLease().GetEpoch(), epoch)
	assert.Equal(t, 5*time.Minute, again.GetPool().GetLease().GetTtl().AsDuration(), "clamped to the maximum")

	// Another pool id for the same name is refused.
	other := apiPool("mistral", "a")
	other.PoolId = "custom"
	_, err = e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: other})
	requireError(t, err, codes.AlreadyExists, gds.ReasonPoolNameTaken)
	// So is a pool id that another cluster's pool uses.
	moved := apiPool("other", "b")
	moved.PoolId = id
	_, err = e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: moved})
	requireError(t, err, codes.AlreadyExists, gds.ReasonPoolNameTaken)

	// The re-registration replaced the spec and waits for a heartbeat.
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_REGISTERED, again.GetPool().GetState())
	assert.Zero(t, again.GetPool().GetWorkerCount())
	e.clock.Advance(time.Second)
	_, err = e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{
		Clusterset: cs, PoolId: id, Epoch: again.GetPool().GetLease().GetEpoch(), WorkerCount: &workers,
	})
	require.NoError(t, err)

	// Draining through the overlay.
	upd, err := e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: id, Generation: registry.NextGeneration(0, e.clock.Now()) + 5,
		Pool:       &gdsv1.Pool{DesiredState: gdsv1.PoolState_POOL_STATE_DRAINING, WorkerCount: 99},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"desired_state"}},
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, upd.GetPool().GetState())
	assert.Equal(t, int32(8), upd.GetPool().GetWorkerCount(), "only masked fields change")
	_, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: id, Generation: 2,
		Pool: &gdsv1.Pool{}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"worker_count"}},
	})
	requireError(t, err, codes.FailedPrecondition, gds.ReasonStaleGeneration)
	_, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: id, Pool: &gdsv1.Pool{}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
	})
	st := requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
	assert.Contains(t, badRequestFields(st), "update_mask")
	_, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: id, Pool: &gdsv1.Pool{Location: &gdsv1.Location{Cluster: "b"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"location"}},
	})
	st = requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
	assert.Contains(t, badRequestFields(st), "pool.location.cluster")
	_, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{Clusterset: cs, PoolId: "unknown", Pool: &gdsv1.Pool{}})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)

	// Every masked path, and an empty mask replacing every field.
	full := apiPool("mistral", "a")
	full.WorkerCount, full.ReadyWorkerCount, full.DesiredState = 4, 4, gdsv1.PoolState_POOL_STATE_ACTIVE
	full.Services = []*gdsv1.ServiceRef{{Namespace: "ns", Name: "fe", Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI}}
	full.Location.Zones = []string{"z1"}
	upd, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: id, Pool: full, UpdateMask: &fieldmaskpb.FieldMask{Paths: updatePaths},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"z1"}, upd.GetPool().GetLocation().GetZones())
	upd, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{Clusterset: cs, PoolId: id, Pool: apiPool("mistral", "a")})
	require.NoError(t, err)
	assert.Empty(t, upd.GetPool().GetServices())
	assert.Equal(t, int32(0), upd.GetPool().GetWorkerCount())

	// Filters.
	_, err = e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("llama", "b")})
	require.NoError(t, err)
	list, err := e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs})
	require.NoError(t, err)
	assert.Len(t, list.GetPools(), 2)
	list, err = e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Cluster: "b"})
	require.NoError(t, err)
	require.Len(t, list.GetPools(), 1)
	assert.Equal(t, "llama", list.GetPools()[0].GetName())
	list, err = e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Model: "mistral-7b", States: []gdsv1.PoolState{gdsv1.PoolState_POOL_STATE_ACTIVE}})
	require.NoError(t, err)
	assert.Len(t, list.GetPools(), 1)
	list, err = e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Region: "eu-west-1"})
	require.NoError(t, err)
	assert.Empty(t, list.GetPools())
	_, err = e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, States: []gdsv1.PoolState{gdsv1.PoolState_POOL_STATE_UNSPECIFIED}})
	requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)

	// Deregistration leaves a GONE tombstone.
	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	got, err = e.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_GONE, got.GetPool().GetState())
	_, err = e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: id})
	require.NoError(t, err, "idempotent")
	_, err = e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{Clusterset: cs, PoolId: id, Pool: &gdsv1.Pool{}})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)
	_, err = e.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs, PoolId: "unknown"})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)
}

func labelledExport(t *testing.T, e *env, cluster, ns, name string, labels, annotations map[string]string, ports ...*gdsv1.ServicePort) {
	t.Helper()
	s := spec(gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, t0, labels, ports...)
	s.Annotations = annotations
	_, err := e.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{
		Clusterset: cs, Cluster: cluster, Namespace: ns, Name: name, Spec: s,
	})
	require.NoError(t, err)
	_, err = e.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{
		Clusterset: cs, Cluster: cluster, Namespace: ns, Name: name, Endpoints: endpoints(true, true),
	})
	require.NoError(t, err)
}

func TestLabelledPoolAndOverlay(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.register(t, "a", "us-east-1")
	labelledExport(t, e, "a", "dyn", "frontend",
		map[string]string{gds.LabelExport: "true", gds.LabelRole: gds.RolePoolFrontend, gds.LabelPool: "demo"},
		map[string]string{gds.AnnotationPoolModels: "llama-3", gds.AnnotationHardwareClass: "b200"},
		&gdsv1.ServicePort{Name: "http", Port: 8000})

	list, err := e.pools.ListPools(ctx(t), &gdsv1.ListPoolsRequest{Clusterset: cs, Model: "llama-3", IncludeEndpoints: true})
	require.NoError(t, err)
	require.Len(t, list.GetPools(), 1)
	p := list.GetPools()[0]
	assert.Equal(t, "demo", p.GetName())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, p.GetState())
	assert.Equal(t, int32(2), p.GetReadyEndpointCount())
	require.Len(t, p.GetFrontends(), 1)
	assert.Equal(t, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, p.GetFrontends()[0].GetRole())
	assert.Equal(t, "frontend", p.GetFrontends()[0].GetService().GetName())
	assert.Len(t, p.GetEndpoints()[0].GetEndpoints(), 2)

	// A label-only pool has no lease to renew.
	_, err = e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{Clusterset: cs, PoolId: p.GetPoolId(), Epoch: 1})
	requireError(t, err, codes.NotFound, gds.ReasonNotFound)

	// The overlay drains it.
	upd, err := e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{
		Clusterset: cs, PoolId: p.GetPoolId(),
		Pool:       &gdsv1.Pool{DesiredState: gdsv1.PoolState_POOL_STATE_DRAINING},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"desired_state"}},
	})
	require.NoError(t, err)
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_DRAINING, upd.GetPool().GetState())
	assert.Equal(t, "b200", upd.GetPool().GetHardwareClass(), "label metadata stays")
	assert.Nil(t, upd.GetPool().GetLease())

	// Registering the same (cluster, name) joins the label-derived pool.
	reg, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: &gdsv1.Pool{
		Name: "demo", Location: &gdsv1.Location{Cluster: "a"},
	}})
	require.NoError(t, err)
	assert.Equal(t, p.GetPoolId(), reg.GetPool().GetPoolId())
	assert.Equal(t, gdsv1.PoolState_POOL_STATE_ACTIVE, reg.GetPool().GetState(), "registration resets the overlay")
}

func TestRegisterPoolSeesOtherReplicaWrites(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	other := apiPool("mistral", "a")
	other.PoolId = "custom"
	_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: other})
	require.NoError(t, err)
	// Another replica's view: a fresh one over the same store.
	e.view.ApplyPoolDelete(storePoolKey("a", "custom"))
	e.clock.Advance(time.Minute)
	reg, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("mistral", "a")})
	require.NoError(t, err)
	assert.Equal(t, "custom", reg.GetPool().GetPoolId(), "the name's holder is found after a reload")
}

func storePoolKey(cluster, id string) store.PoolKey {
	return store.PoolKey{ClusterSet: cs, Cluster: cluster, PoolID: id}
}

func TestRouters(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.register(t, "a", "us-east-1")
	labelledExport(t, e, "a", "llm", "backend-router",
		map[string]string{gds.LabelRole: gds.RoleRouter}, map[string]string{gds.AnnotationDialScheme: "https"},
		&gdsv1.ServicePort{Name: "grpc", Port: 50071})
	list, err := e.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs, Region: "us-east-1"})
	require.NoError(t, err)
	require.Len(t, list.GetRouters(), 1)
	r := list.GetRouters()[0]
	assert.Equal(t, int32(50071), r.GetGrpcPort())
	assert.Equal(t, "https", r.GetDialScheme())
	assert.Len(t, r.GetReplicas(), 2)
	list, err = e.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs, Cluster: "b"})
	require.NoError(t, err)
	assert.Empty(t, list.GetRouters())
}

func TestServerInfo(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	info, err := e.info.GetServerInfo(ctx(t), &gdsv1.GetServerInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", info.GetRegion())
	assert.Equal(t, "v1.2.3", info.GetVersion())
	assert.Equal(t, "e1", info.GetViewEpoch())
	assert.Equal(t, t0, info.GetServerTime().AsTime())
	require.Len(t, info.GetEndpoints(), 1)
	assert.True(t, info.GetEndpoints()[0].GetPrimary())
}

func TestValidation(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	cases := []struct {
		name   string
		call   func() error
		fields []string
	}{
		{"cluster names", func() error {
			_, err := e.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: "Prod", Cluster: ""})
			return err
		}, []string{"clusterset", "cluster"}},
		{"negative ttl", func() error {
			_, err := e.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: cs, Cluster: "a", LeaseTtl: durationpb.New(-time.Second)})
			return err
		}, []string{"lease_ttl"}},
		{"heartbeat epoch", func() error {
			_, err := e.clusters.HeartbeatCluster(ctx(t), &gdsv1.HeartbeatClusterRequest{Clusterset: cs, Cluster: "a"})
			return err
		}, []string{"epoch"}},
		{"export spec", func() error {
			_, err := e.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{
				Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web",
				Spec: &gdsv1.ServiceExportSpec{
					Ports:                 []*gdsv1.ServicePort{{Port: 0}, {Port: 80, Protocol: 9}},
					IpFamilies:            []gdsv1.IPFamily{gdsv1.IPFamily_IP_FAMILY_UNSPECIFIED},
					SessionAffinityConfig: &gdsv1.SessionAffinityConfig{ClientIp: &gdsv1.ClientIPConfig{TimeoutSeconds: -1}},
					Labels:                map[string]string{"": "x"},
					Annotations:           map[string]string{"a": string(make([]byte, gds.MaxAnnotationBytes))},
				},
			})
			return err
		}, []string{"spec.type", "spec.ports[0].port", "spec.ports[1].protocol", "spec.ip_families[0]",
			"spec.session_affinity_config.client_ip.timeout_seconds", "spec.labels", "spec.annotations"}},
		{"missing spec", func() error {
			_, err := e.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web"})
			return err
		}, []string{"spec"}},
		{"endpoints", func() error {
			_, err := e.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{
				Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web",
				Endpoints: []*gdsv1.Endpoint{{Ports: []*gdsv1.EndpointPort{{Port: 70000}}}},
			})
			return err
		}, []string{"endpoints[0].addresses", "endpoints[0].ports[0].port"}},
		{"pool", func() error {
			_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: &gdsv1.Pool{
				PoolId:       "bad/id",
				Name:         "Bad",
				WorkerCount:  -1,
				DesiredState: gdsv1.PoolState_POOL_STATE_GONE,
				Models:       []*gdsv1.ModelServed{{}},
				Frontends: []*gdsv1.FrontendEndpoint{
					{Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI},
					{Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, Url: "ftp://x"},
					{Service: &gdsv1.ServiceRef{Namespace: "ns"}},
				},
				Services: []*gdsv1.ServiceRef{{Name: "x"}},
			}})
			return err
		}, []string{"pool.name", "pool.location.cluster", "pool.worker_count", "pool.desired_state", "pool.models[0].model_id",
			"pool.frontends[0]", "pool.frontends[1].url", "pool.frontends[2].role", "pool.frontends[2].service.name",
			"pool.services[0].namespace", "pool.pool_id"}},
		{"nil pool", func() error {
			_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs})
			return err
		}, []string{"pool"}},
		{"pool heartbeat", func() error {
			neg := int32(-1)
			_, err := e.pools.HeartbeatPool(ctx(t), &gdsv1.HeartbeatPoolRequest{Clusterset: cs, WorkerCount: &neg, ReadyWorkerCount: &neg})
			return err
		}, []string{"pool_id", "epoch", "worker_count", "ready_worker_count"}},
		{"update pool", func() error {
			_, err := e.pools.UpdatePool(ctx(t), &gdsv1.UpdatePoolRequest{Clusterset: cs, PoolId: "p"})
			return err
		}, []string{"pool"}},
		{"list imports", func() error {
			_, err := e.imports.ListServiceImports(ctx(t), &gdsv1.ListServiceImportsRequest{Clusterset: cs, Namespace: "Bad"})
			return err
		}, []string{"namespace"}},
		{"get import", func() error {
			_, err := e.imports.GetServiceImport(ctx(t), &gdsv1.GetServiceImportRequest{Clusterset: cs})
			return err
		}, []string{"namespace", "name"}},
		{"routers", func() error {
			_, err := e.pools.ListRouters(ctx(t), &gdsv1.ListRoutersRequest{Clusterset: cs, Cluster: "B"})
			return err
		}, []string{"cluster"}},
		{"clusters", func() error {
			_, err := e.clusters.ListClusters(ctx(t), &gdsv1.ListClustersRequest{})
			return err
		}, []string{"clusterset"}},
		{"get cluster", func() error {
			_, err := e.clusters.GetCluster(ctx(t), &gdsv1.GetClusterRequest{Clusterset: cs})
			return err
		}, []string{"cluster"}},
		{"deregister cluster", func() error {
			_, err := e.clusters.DeregisterCluster(ctx(t), &gdsv1.DeregisterClusterRequest{Clusterset: cs})
			return err
		}, []string{"cluster"}},
		{"exports", func() error {
			_, err := e.exports.ListServiceExports(ctx(t), &gdsv1.ListServiceExportsRequest{Clusterset: cs})
			return err
		}, []string{"cluster"}},
		{"delete export", func() error {
			_, err := e.exports.DeleteServiceExport(ctx(t), &gdsv1.DeleteServiceExportRequest{Clusterset: cs})
			return err
		}, []string{"cluster", "namespace", "name"}},
		{"get pool", func() error {
			_, err := e.pools.GetPool(ctx(t), &gdsv1.GetPoolRequest{Clusterset: cs})
			return err
		}, []string{"pool_id"}},
		{"deregister pool", func() error {
			_, err := e.pools.DeregisterPool(ctx(t), &gdsv1.DeregisterPoolRequest{Clusterset: cs, PoolId: "a b"})
			return err
		}, []string{"pool_id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := requireError(t, tc.call(), codes.InvalidArgument, gds.ReasonInvalidArgument)
			fields := badRequestFields(st)
			for _, f := range tc.fields {
				assert.Contains(t, fields, f)
			}
		})
	}
}

func TestStoreUnavailable(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	e.register(t, "a", "us-east-1")
	require.NoError(t, e.store.Close())
	_, err := e.clusters.RegisterCluster(ctx(t), &gdsv1.RegisterClusterRequest{Clusterset: cs, Cluster: "a"})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
	_, err = e.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{
		Clusterset: cs, Cluster: "b", Namespace: "ns", Name: "web",
		Spec: spec(gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, t0, nil, httpPort),
	})
	requireError(t, err, codes.Unavailable, gds.ReasonStoreUnavailable)
	for _, call := range []func() error{
		func() error {
			_, err := e.clusters.HeartbeatCluster(ctx(t), &gdsv1.HeartbeatClusterRequest{Clusterset: cs, Cluster: "a", Epoch: 1})
			return err
		},
		func() error {
			_, err := e.clusters.DeregisterCluster(ctx(t), &gdsv1.DeregisterClusterRequest{Clusterset: cs, Cluster: "a"})
			return err
		},
		func() error {
			_, err := e.exports.PutServiceExport(ctx(t), &gdsv1.PutServiceExportRequest{
				Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web",
				Spec: spec(gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, t0, nil, httpPort),
			})
			return err
		},
		func() error {
			_, err := e.exports.ReplaceEndpoints(ctx(t), &gdsv1.ReplaceEndpointsRequest{Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web"})
			return err
		},
		func() error {
			_, err := e.exports.DeleteServiceExport(ctx(t), &gdsv1.DeleteServiceExportRequest{Clusterset: cs, Cluster: "a", Namespace: "ns", Name: "web"})
			return err
		},
		func() error {
			_, err := e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("x", "a")})
			return err
		},
	} {
		requireError(t, call(), codes.Unavailable, gds.ReasonStoreUnavailable)
	}
}

func TestWatches(t *testing.T) {
	e := newEnv(t, view.HubOptions{BookmarkInterval: 5 * time.Second, MaxWatchers: 4})
	e.register(t, "a", "us-east-1")

	pools, err := e.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs, Model: "mistral"})
	require.NoError(t, err)
	ev, err := pools.Recv()
	require.NoError(t, err)
	assert.Equal(t, gdsv1.EventType_EVENT_TYPE_SNAPSHOT_START, ev.GetType())
	ev, err = pools.Recv()
	require.NoError(t, err)
	assert.Equal(t, gdsv1.EventType_EVENT_TYPE_SNAPSHOT_END, ev.GetType())
	rev := ev.GetRevision()

	_, err = e.pools.RegisterPool(ctx(t), &gdsv1.RegisterPoolRequest{Clusterset: cs, Pool: apiPool("mistral", "a")})
	require.NoError(t, err)
	ev, err = pools.Recv()
	require.NoError(t, err)
	assert.Equal(t, gdsv1.EventType_EVENT_TYPE_UPSERT, ev.GetType())
	assert.Equal(t, "mistral", ev.GetPool().GetName())

	e.clock.BlockUntil(1)
	e.clock.Advance(5 * time.Second)
	ev, err = pools.Recv()
	require.NoError(t, err)
	assert.Equal(t, gdsv1.EventType_EVENT_TYPE_BOOKMARK, ev.GetType())

	// Resume from the snapshot revision replays the upsert.
	resumed, err := e.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs, Model: "mistral", ResumeRevision: rev})
	require.NoError(t, err)
	ev, err = resumed.Recv()
	require.NoError(t, err)
	assert.Equal(t, gdsv1.EventType_EVENT_TYPE_UPSERT, ev.GetType())

	clusters, err := e.clusters.WatchClusters(ctx(t), &gdsv1.WatchClustersRequest{Clusterset: cs})
	require.NoError(t, err)
	_, err = clusters.Recv()
	require.NoError(t, err)
	ev2, err := clusters.Recv()
	require.NoError(t, err)
	assert.Equal(t, "a", ev2.GetCluster().GetCluster())

	imports, err := e.imports.WatchServiceImports(ctx(t), &gdsv1.WatchServiceImportsRequest{Clusterset: cs, IncludeEndpoints: true})
	require.NoError(t, err)
	_, err = imports.Recv()
	require.NoError(t, err)
	labelledExport(t, e, "a", "ns", "web", nil, nil, httpPort)
	for {
		ev3, err := imports.Recv()
		require.NoError(t, err)
		if ev3.GetType() == gdsv1.EventType_EVENT_TYPE_UPSERT && ev3.GetServiceImport().GetEndpoints()[0].GetEndpointCount() > 0 {
			assert.Len(t, ev3.GetServiceImport().GetEndpoints()[0].GetEndpoints(), 2)
			break
		}
	}

	// The fifth watcher is refused.
	routers, err := e.pools.WatchRouters(ctx(t), &gdsv1.WatchRoutersRequest{Clusterset: cs})
	require.NoError(t, err)
	_, err = routers.Recv()
	requireError(t, err, codes.ResourceExhausted, gds.ReasonTooManyWatchers)

	// Shutdown ends every watch with SHUTTING_DOWN.
	e.hub.Close()
	for {
		_, err = pools.Recv()
		if err != nil {
			break
		}
	}
	requireError(t, err, codes.Unavailable, gds.ReasonShuttingDown)
	assert.NotEqual(t, io.EOF, err)
}

func TestWatchValidation(t *testing.T) {
	e := newEnv(t, view.HubOptions{})
	c, err := e.clusters.WatchClusters(ctx(t), &gdsv1.WatchClustersRequest{})
	require.NoError(t, err)
	_, err = c.Recv()
	requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
	i, err := e.imports.WatchServiceImports(ctx(t), &gdsv1.WatchServiceImportsRequest{Clusterset: cs, Namespace: "BAD"})
	require.NoError(t, err)
	_, err = i.Recv()
	requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
	p, err := e.pools.WatchPools(ctx(t), &gdsv1.WatchPoolsRequest{Clusterset: cs, Cluster: "BAD"})
	require.NoError(t, err)
	_, err = p.Recv()
	requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
	r, err := e.pools.WatchRouters(ctx(t), &gdsv1.WatchRoutersRequest{})
	require.NoError(t, err)
	_, err = r.Recv()
	requireError(t, err, codes.InvalidArgument, gds.ReasonInvalidArgument)
}

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

package status

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/progress"
)

// captureSink records all emitted events for assertion.
type captureSink struct {
	events []progress.Event
}

func (c *captureSink) Emit(_ context.Context, e progress.Event) error {
	c.events = append(c.events, e)
	return nil
}

func (c *captureSink) Close() error { return nil }

// fakeSIS is a test double for ClusterLister.
type fakeSIS struct {
	clusters []client.SISCluster
	err      error
}

func (f *fakeSIS) ListClusters(_ context.Context, _, _ string) ([]client.SISCluster, error) {
	return f.clusters, f.err
}

// ptrInt32 returns a pointer to the given int32 value.
func ptrInt32(v int32) *int32 { return &v }

// fixedNow is a deterministic clock for tests.
var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// buildAllReadyKube constructs a fake kube client with all default components
// in their respective namespaces, each with Spec.Replicas=1 and
// Status.ReadyReplicas=1.
func buildAllReadyKube(components []ComponentSpec) *fake.Clientset {
	var objs []runtime.Object
	creationTime := metav1.NewTime(fixedNow.Add(-1 * time.Hour))

	for _, sp := range components {
		switch sp.Kind {
		case "deployment":
			objs = append(objs, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:              sp.Resource,
					Namespace:         sp.Namespace,
					CreationTimestamp: creationTime,
				},
				Spec:   appsv1.DeploymentSpec{Replicas: ptrInt32(1)},
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			})
		case "statefulset":
			objs = append(objs, &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:              sp.Resource,
					Namespace:         sp.Namespace,
					CreationTimestamp: creationTime,
				},
				Spec:   appsv1.StatefulSetSpec{Replicas: ptrInt32(1)},
				Status: appsv1.StatefulSetStatus{ReadyReplicas: 1},
			})
		}
	}
	return fake.NewSimpleClientset(objs...)
}

func TestCollector_Healthy(t *testing.T) {
	components := DefaultComponents()
	kube := buildAllReadyKube(components)
	sis := &fakeSIS{clusters: []client.SISCluster{{ClusterID: "cl-1", ClusterName: "ncp-local"}}}
	sink := &captureSink{}

	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	err := coll.Collect(context.Background(), sink)
	require.NoError(t, err)

	// Event order: 1 Snapshot + 9 ComponentHealth + 1 ClusterRow = 11 events.
	// (NVCA Worker removed in iteration #2 after dev-VM E2E found that the
	// nvcf-self-managed-stack compute plane is solely the nvca-operator
	// deployment; worker pods are operator-managed lifecycle resources.)
	require.Len(t, sink.events, 11)

	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok, "first event must be Snapshot")
	assert.Equal(t, "healthy", snap.Verdict)
	assert.Equal(t, "ncp-local", snap.Cluster)

	// All 9 ComponentHealth events should be Healthy with no Role (single-cluster mode).
	healthCount := 0
	for _, e := range sink.events[1:10] {
		ch, ok := e.(progress.ComponentHealth)
		require.True(t, ok, "events 1..9 must be ComponentHealth, got %T", e)
		assert.True(t, ch.Healthy, "component %q should be healthy", ch.Name)
		assert.Empty(t, ch.Role, "single-cluster mode: Role should be empty")
		healthCount++
	}
	assert.Equal(t, 9, healthCount)

	// Last event is the ClusterRow — IsCurrent because name matches Cluster.
	cr, ok := sink.events[10].(progress.ClusterRow)
	require.True(t, ok, "last event must be ClusterRow")
	assert.Equal(t, "ncp-local", cr.Name)
	assert.True(t, cr.Healthy)
	assert.True(t, cr.IsCurrent, "ClusterRow matching Cluster should have IsCurrent=true")
	assert.Empty(t, cr.Context, "no ComputePlaneContext set → Context should be empty")
}

func TestCollector_DegradedOneNotReady(t *testing.T) {
	components := DefaultComponents()
	kube := buildAllReadyKube(components)

	// Override Cassandra to be not-ready (Spec.Replicas=1, ReadyReplicas=0).
	ctx := context.Background()
	cassandra, err := kube.AppsV1().StatefulSets("cassandra-system").Get(ctx, "cassandra", metav1.GetOptions{})
	require.NoError(t, err)
	cassandra.Status.ReadyReplicas = 0
	_, err = kube.AppsV1().StatefulSets("cassandra-system").UpdateStatus(ctx, cassandra, metav1.UpdateOptions{})
	require.NoError(t, err)

	sis := &fakeSIS{clusters: []client.SISCluster{{ClusterName: "ncp-local"}}}
	sink := &captureSink{}

	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	err = coll.Collect(context.Background(), sink)
	require.NoError(t, err)

	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok)
	assert.Equal(t, "degraded", snap.Verdict)

	// Find the Cassandra ComponentHealth row.
	var cassandraHealth *progress.ComponentHealth
	for _, e := range sink.events {
		if ch, ok := e.(progress.ComponentHealth); ok && ch.Name == "Cassandra" {
			chCopy := ch
			cassandraHealth = &chCopy
			break
		}
	}
	require.NotNil(t, cassandraHealth, "Cassandra ComponentHealth not found")
	assert.False(t, cassandraHealth.Healthy)
	assert.Equal(t, 0, cassandraHealth.Ready)
	assert.Equal(t, 1, cassandraHealth.Total)
}

func TestCollector_UnknownSISDown(t *testing.T) {
	components := DefaultComponents()
	kube := buildAllReadyKube(components)
	sis := &fakeSIS{err: errors.New("connection refused")}
	sink := &captureSink{}

	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	err := coll.Collect(context.Background(), sink)
	require.NoError(t, err)

	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok)
	assert.Equal(t, "unknown", snap.Verdict)

	// Should have 9 workload ComponentHealth + 1 SIS diagnostic row + 0 ClusterRow.
	assert.Len(t, sink.events, 11)

	// Workload component health events should be healthy.
	for _, e := range sink.events[1:10] {
		ch, ok := e.(progress.ComponentHealth)
		require.True(t, ok, "expected ComponentHealth, got %T", e)
		assert.True(t, ch.Healthy)
	}
	sisDiag, ok := sink.events[10].(progress.ComponentHealth)
	require.True(t, ok, "expected SIS diagnostic ComponentHealth, got %T", sink.events[10])
	assert.Equal(t, "SIS Cluster List", sisDiag.Name)
	assert.False(t, sisDiag.Healthy)
	assert.Contains(t, sisDiag.Message, "connection refused")
}

// TestCollector_UnknownWinsOverDegraded pins verdict precedence: when SIS is
// unreachable AND a component is not-ready, "unknown" wins. We don't trust
// our cluster view without SIS, so reporting "degraded" off a partial picture
// would be misleading.
func TestCollector_UnknownWinsOverDegraded(t *testing.T) {
	components := DefaultComponents()
	kube := buildAllReadyKube(components)

	ctx := context.Background()
	cassandra, err := kube.AppsV1().StatefulSets("cassandra-system").Get(ctx, "cassandra", metav1.GetOptions{})
	require.NoError(t, err)
	cassandra.Status.ReadyReplicas = 0
	_, err = kube.AppsV1().StatefulSets("cassandra-system").UpdateStatus(ctx, cassandra, metav1.UpdateOptions{})
	require.NoError(t, err)

	sis := &fakeSIS{err: errors.New("connection refused")}
	sink := &captureSink{}

	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	require.NoError(t, coll.Collect(context.Background(), sink))
	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok)
	assert.Equal(t, "unknown", snap.Verdict)
}

func TestCollector_NotFoundComponent(t *testing.T) {
	// Use an empty kube client — no resources exist.
	kube := fake.NewSimpleClientset()
	sis := &fakeSIS{clusters: []client.SISCluster{{ClusterName: "ncp-local"}}}
	sink := &captureSink{}

	// Single component to keep the test focused. No Role → single-cluster mode.
	components := []ComponentSpec{
		{Name: "SIS", Namespace: "sis-system", Kind: "statefulset", Resource: "sis"},
	}

	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	err := coll.Collect(context.Background(), sink)
	require.NoError(t, err)

	// Verdict must be degraded because the component wasn't found.
	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok)
	assert.Equal(t, "degraded", snap.Verdict)

	// The single ComponentHealth should be !Healthy with a non-empty error message.
	ch, ok := sink.events[1].(progress.ComponentHealth)
	require.True(t, ok)
	assert.False(t, ch.Healthy)
	assert.NotEmpty(t, ch.Message)
}

// controlPlaneOnlyComponents returns the control-plane subset of DefaultComponents.
func controlPlaneOnlyComponents() []ComponentSpec {
	var out []ComponentSpec
	for _, sp := range DefaultComponents() {
		if sp.Role == "control-plane" {
			out = append(out, sp)
		}
	}
	return out
}

// computePlaneOnlyComponents returns the compute-plane subset of DefaultComponents.
func computePlaneOnlyComponents() []ComponentSpec {
	var out []ComponentSpec
	for _, sp := range DefaultComponents() {
		if sp.Role == "compute-plane" {
			out = append(out, sp)
		}
	}
	return out
}

// TestCollector_SplitClusterEmitsRoleLabels verifies that when ControlPlaneKube
// and ComputePlaneKube are distinct clients, ComponentHealth events are tagged
// with "control-plane" or "compute-plane" Role, ClusterRow surfaces IsCurrent
// on the matching cluster, and Context is set on the IsCurrent row when
// ComputePlaneContext is configured.
func TestCollector_SplitClusterEmitsRoleLabels(t *testing.T) {
	components := DefaultComponents()
	cpComponents := controlPlaneOnlyComponents()
	gpuComponents := computePlaneOnlyComponents()

	cpKube := buildAllReadyKube(cpComponents)
	gpuKube := buildAllReadyKube(gpuComponents)
	sis := &fakeSIS{clusters: []client.SISCluster{
		{ClusterName: "ncp-local"},
		{ClusterName: "gpu-cluster-2"},
	}}
	sink := &captureSink{}

	coll := &Collector{
		ControlPlaneKube:    cpKube,
		ComputePlaneKube:    gpuKube,
		SIS:                 sis,
		SISURL:              "http://sis.example",
		NCAID:               "test-nca",
		Cluster:             "ncp-local",
		ComputePlaneContext: "admin@gpu1",
		Components:          components,
		NowFunc:             func() time.Time { return fixedNow },
	}

	require.NoError(t, coll.Collect(context.Background(), sink))

	// 1 Snapshot + 10 ComponentHealth + 2 ClusterRow = 13 events.
	require.Len(t, sink.events, 12)

	snap, ok := sink.events[0].(progress.Snapshot)
	require.True(t, ok, "first event must be Snapshot")
	assert.Equal(t, "healthy", snap.Verdict)

	// Collect all ComponentHealth events and verify Role labels.
	var cpHealths, gpuHealths []progress.ComponentHealth
	for _, e := range sink.events[1:10] {
		ch, ok := e.(progress.ComponentHealth)
		require.True(t, ok, "events 1..10 must be ComponentHealth, got %T", e)
		assert.True(t, ch.Healthy, "component %q should be healthy", ch.Name)
		switch ch.Role {
		case "control-plane":
			cpHealths = append(cpHealths, ch)
		case "compute-plane":
			gpuHealths = append(gpuHealths, ch)
		default:
			t.Errorf("expected Role to be control-plane or compute-plane, got %q for component %q", ch.Role, ch.Name)
		}
	}
	assert.Len(t, cpHealths, 8, "expected 8 control-plane components")
	assert.Len(t, gpuHealths, 1, "expected 1 compute-plane component (NVCA Operator only after iter#2)")

	// ClusterRow events: ncp-local should be IsCurrent with Context set.
	var currentRow, otherRow *progress.ClusterRow
	for _, e := range sink.events[10:] {
		cr, ok := e.(progress.ClusterRow)
		require.True(t, ok, "events 11+ must be ClusterRow, got %T", e)
		crCopy := cr
		if cr.IsCurrent {
			currentRow = &crCopy
		} else {
			otherRow = &crCopy
		}
	}
	require.NotNil(t, currentRow, "expected one ClusterRow with IsCurrent=true")
	assert.Equal(t, "ncp-local", currentRow.Name)
	assert.Equal(t, "admin@gpu1", currentRow.Context)

	require.NotNil(t, otherRow, "expected one ClusterRow with IsCurrent=false")
	assert.Equal(t, "gpu-cluster-2", otherRow.Name)
	assert.Empty(t, otherRow.Context, "non-current row should have empty Context")
}

// TestCollector_SingleClusterPreserved verifies that single-cluster mode
// (ComputePlaneKube=nil, using legacy Kube field) emits ComponentHealth events
// with Role="" and ClusterRow with IsCurrent/Context behavior unchanged.
func TestCollector_SingleClusterPreserved(t *testing.T) {
	components := DefaultComponents()
	kube := buildAllReadyKube(components)
	sis := &fakeSIS{clusters: []client.SISCluster{{ClusterName: "ncp-local"}}}
	sink := &captureSink{}

	// Legacy single-client construction — no ControlPlaneKube/ComputePlaneKube.
	coll := &Collector{
		Kube:       kube,
		SIS:        sis,
		SISURL:     "http://sis.example",
		NCAID:      "test-nca",
		Cluster:    "ncp-local",
		Components: components,
		NowFunc:    func() time.Time { return fixedNow },
	}

	require.NoError(t, coll.Collect(context.Background(), sink))

	// All ComponentHealth events must have Role="" (no split in single-cluster mode).
	for _, e := range sink.events[1:10] {
		ch, ok := e.(progress.ComponentHealth)
		require.True(t, ok)
		assert.Empty(t, ch.Role, "single-cluster mode must emit Role=\"\"")
	}

	// ClusterRow still sets IsCurrent when name matches Cluster.
	cr, ok := sink.events[10].(progress.ClusterRow)
	require.True(t, ok, "last event must be ClusterRow")
	assert.True(t, cr.IsCurrent)
	assert.Empty(t, cr.Context, "no ComputePlaneContext → Context stays empty")
}

// TestCollector_SplitClusterIsCurrent_ContextOnlyOnCurrent verifies that when
// there are multiple SIS clusters, only the current cluster gets Context set.
func TestCollector_SplitClusterIsCurrent_ContextOnlyOnCurrent(t *testing.T) {
	components := DefaultComponents()
	cpKube := buildAllReadyKube(controlPlaneOnlyComponents())
	gpuKube := buildAllReadyKube(computePlaneOnlyComponents())

	sis := &fakeSIS{clusters: []client.SISCluster{
		{ClusterName: "cluster-a"},
		{ClusterName: "cluster-b"},
		{ClusterName: "cluster-c"},
	}}
	sink := &captureSink{}

	coll := &Collector{
		ControlPlaneKube:    cpKube,
		ComputePlaneKube:    gpuKube,
		SIS:                 sis,
		SISURL:              "http://sis.example",
		NCAID:               "test-nca",
		Cluster:             "cluster-b",
		ComputePlaneContext: "admin@cluster-b",
		Components:          components,
		NowFunc:             func() time.Time { return fixedNow },
	}

	require.NoError(t, coll.Collect(context.Background(), sink))

	// Find the ClusterRow events (after 1 Snapshot + 10 ComponentHealth).
	for _, e := range sink.events[11:] {
		cr, ok := e.(progress.ClusterRow)
		require.True(t, ok)
		if cr.Name == "cluster-b" {
			assert.True(t, cr.IsCurrent)
			assert.Equal(t, "admin@cluster-b", cr.Context)
		} else {
			assert.False(t, cr.IsCurrent, "cluster %q should not be IsCurrent", cr.Name)
			assert.Empty(t, cr.Context, "cluster %q should have empty Context", cr.Name)
		}
	}
}

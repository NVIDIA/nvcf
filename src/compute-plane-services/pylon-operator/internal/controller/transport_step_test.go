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

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

// withoutReadyTransport drops the pre-made ready Deployment, so the
// transport step creates it.
func withoutReadyTransport(objs []client.Object) []client.Object {
	out := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		if _, ok := o.(*appsv1.Deployment); !ok {
			out = append(out, o)
		}
	}
	return out
}

func (f *fixture) deployment() *appsv1.Deployment {
	f.t.Helper()
	d := &appsv1.Deployment{}
	require.NoError(f.t, f.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "pylon-llama"}, d))
	return d
}

// setReadyReplicas plays the Deployment controller.
func (f *fixture) setReadyReplicas(n int32) {
	f.t.Helper()
	d := f.deployment()
	d.Status.Replicas, d.Status.ReadyReplicas = n, n
	require.NoError(f.t, f.client.Status().Update(context.Background(), d))
}

// transportReplicasReady returns nvcf_pylon_operator_transport_replicas_ready
// for endpoint, and whether the series exists.
func (f *fixture) transportReplicasReady(endpoint string) (float64, bool) {
	f.t.Helper()
	families, err := f.registry.Gather()
	require.NoError(f.t, err)
	for _, fam := range families {
		if fam.GetName() != "nvcf_pylon_operator_transport_replicas_ready" {
			continue
		}
		for _, m := range fam.GetMetric() {
			if m.GetLabel()[0].GetValue() == endpoint {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func (f *fixture) assertTransportConditions(status metav1.ConditionStatus, reason, message string) {
	f.t.Helper()
	for _, ct := range []pylonv1alpha1.ConditionType{pylonv1alpha1.ConditionTransportReady, pylonv1alpha1.ConditionRegistered} {
		c := f.condition(ct)
		assert.Equal(f.t, status, c.Status, ct)
		assert.Equal(f.t, reason, c.Reason, ct)
		if message != "" {
			assert.Equal(f.t, message, c.Message, ct)
		}
	}
}

func TestTransportStepFastNegatives(t *testing.T) {
	f := newFixture(t, withObjects(withoutReadyTransport(healthyObjects())...))

	// Created, no pod ready yet.
	f.mustReconcile()
	assert.Equal(t, int32(1), *f.deployment().Spec.Replicas)
	f.assertTransportConditions(metav1.ConditionFalse, "TransportPodsNotRunning", `Transport Deployment "pylon-llama" has no ready pods (0 of 1)`)
	events := f.events()
	assert.Contains(t, events, `Warning TransportPodsNotRunning TransportReady is False: Transport Deployment "pylon-llama" has no ready pods (0 of 1)`)
	assert.Contains(t, events, `Warning TransportPodsNotRunning Registered is False: Transport Deployment "pylon-llama" has no ready pods (0 of 1)`)
	ready, ok := f.transportReplicasReady("models/llama")
	require.True(t, ok)
	assert.Equal(t, 0.0, ready)
	assert.Equal(t, 1.0, f.endpointsGauge(pylonv1alpha1.ConditionTransportReady, metav1.ConditionFalse))

	// The pod is ready: the registration observer owns both again, and no
	// transport pod reports a stream yet.
	f.setReadyReplicas(1)
	f.mustReconcile()
	assert.Equal(t, "TunnelNotConnected", f.condition(pylonv1alpha1.ConditionTransportReady).Reason)
	assert.Equal(t, "Pending", f.condition(pylonv1alpha1.ConditionRegistered).Reason)
	ready, _ = f.transportReplicasReady("models/llama")
	assert.Equal(t, 1.0, ready)
	f.events()

	// Model name mismatch: scaled to zero, both False ScaledToZero.
	f.backend.setModels("mistral/mistral-7b")
	f.mustReconcile()
	assert.Equal(t, int32(0), *f.deployment().Spec.Replicas)
	f.assertTransportConditions(metav1.ConditionFalse, "ScaledToZero",
		`Transport Deployment "pylon-llama" is scaled to zero while Ready is False with reason ModelNameMismatch`)
	f.setReadyReplicas(0)
	f.mustReconcile()
	f.assertTransportConditions(metav1.ConditionFalse, "ScaledToZero", "")

	// Corrected backend: restored, not running until the pod is ready.
	f.backend.setModels(testModel)
	f.mustReconcile()
	assert.Equal(t, int32(1), *f.deployment().Spec.Replicas)
	f.assertTransportConditions(metav1.ConditionFalse, "TransportPodsNotRunning", "")
	f.setReadyReplicas(1)
	f.mustReconcile()
	assert.Equal(t, "Pending", f.condition(pylonv1alpha1.ConditionRegistered).Reason)

	// The endpoint goes away: its transport series goes with it.
	require.NoError(t, f.client.Delete(context.Background(), f.endpoint()))
	f.mustReconcile()
	_, ok = f.transportReplicasReady("models/llama")
	assert.False(t, ok)
}

func TestTransportStepMissingCredential(t *testing.T) {
	f := newFixture(t, withoutCredential(), withObjects(withoutReadyTransport(healthyObjects())...))
	msg := "Cluster credential Secret pylon-operator/pylon-operator-cluster-credential not found; the transport Deployment is not created or updated until it exists"

	f.mustReconcile()
	err := f.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "pylon-llama"}, &appsv1.Deployment{})
	assert.True(t, apierrors.IsNotFound(err), "no Deployment without the credential")
	f.assertTransportConditions(metav1.ConditionFalse, "TransportPodsNotRunning", msg)
	events := f.events()
	assert.Contains(t, events, "Warning ClusterCredentialMissing "+msg)
	for _, e := range events {
		assert.NotContains(t, e, testToken)
	}

	// Still missing: the Warning is not repeated.
	f.mustReconcile()
	assert.Empty(t, f.events())

	// The Secret arrives.
	require.NoError(t, f.client.Create(context.Background(), credential()))
	f.mustReconcile()
	f.deployment()
	f.assertTransportConditions(metav1.ConditionFalse, "TransportPodsNotRunning", `Transport Deployment "pylon-llama" has no ready pods (0 of 1)`)
	for _, e := range f.events() {
		assert.NotContains(t, e, testToken)
	}
}

func TestTransportStepProblemEventOnChange(t *testing.T) {
	f := newFixture(t, withoutCredential(), withObjects(withoutReadyTransport(healthyObjects())...))
	f.mustReconcile()
	f.events()

	// A different problem while TransportReady stays False with the same
	// reason: a new Warning.
	require.NoError(t, f.client.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultClusterCredentialSecret, Namespace: operatorNamespace},
		Data:       map[string][]byte{"wrong-key": []byte(testToken)},
	}))
	f.mustReconcile()
	events := f.events()
	require.NotEmpty(t, events)
	assert.True(t, strings.HasPrefix(events[len(events)-1], "Warning ClusterCredentialMissing Cluster credential Secret pylon-operator/pylon-operator-cluster-credential has no cluster-token key"), events)
}

func TestTransportStepError(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return fmt.Errorf("cache not synced")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}))
	_, err := f.reconcile()
	require.EqualError(t, err, `step transport: reading Deployment "pylon-llama": cache not synced`)
	assert.Equal(t, int64(0), f.endpoint().Status.ObservedGeneration)
	_, ok := f.transportReplicasReady("models/llama")
	assert.False(t, ok)
}

func TestTransportFastNegativeWithoutState(t *testing.T) {
	rc := newReconcileContext(inferenceEndpoint(1), testConfig())
	_, ok := rc.TransportFastNegative()
	assert.False(t, ok, "no transport state")
}

func TestEndpointsForReplicated(t *testing.T) {
	objs := []client.Object{
		endpointNamed("models", "a", "svc"),
		endpointNamed("models", "b", "svc"),
		endpointNamed("team-b", "c", "svc"),
	}
	f := newFixture(t, withObjects(objs...))
	step := &transportStep{reconciler: transport.New(f.client, testConfig()), reader: f.client}
	mapSecret := step.endpointsForReplicated(config.DefaultClusterCredentialSecret)
	ctx := context.Background()

	secret := func(namespace, name string, managed bool) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
		if managed {
			s.Labels = map[string]string{config.ManagedByLabel: config.ManagedBy}
		}
		return s
	}
	assert.ElementsMatch(t, requests("models/a", "models/b", "team-b/c"),
		mapSecret(ctx, secret(operatorNamespace, config.DefaultClusterCredentialSecret, false)), "the source requeues every endpoint")
	assert.ElementsMatch(t, requests("team-b/c"),
		mapSecret(ctx, secret("team-b", config.DefaultClusterCredentialSecret, true)), "a copy requeues its namespace")
	assert.Nil(t, mapSecret(ctx, secret("team-b", config.DefaultClusterCredentialSecret, false)), "not ours")
	assert.Nil(t, mapSecret(ctx, secret(operatorNamespace, "other", false)), "another Secret")

	mapConfigMap := step.endpointsForReplicated("router-ca")
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "router-ca"}}
	assert.Len(t, mapConfigMap(ctx, cm), 3)

	failing := newFixture(t, withObjects(objs...), withInterceptors(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fmt.Errorf("cache not synced")
		},
	}))
	step.reader = failing.client
	assert.Nil(t, mapSecret(ctx, secret(operatorNamespace, config.DefaultClusterCredentialSecret, false)))
}

// withPoolExport runs the fixture with the GDS router source and
// --gds-pool-export.
func withPoolExport() fixtureOption {
	return withConfig(func(c *config.Config) {
		c.RouterSource = config.RouterSourceGDS
		c.GDSURLs = []string{"https://gds.example.test"}
		c.GDSClustersetID = "prod"
		c.GDSPoolExport = true
	})
}

func (f *fixture) poolService() (*corev1.Service, error) {
	s := &corev1.Service{}
	err := f.client.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "pylon-llama"}, s)
	return s, err
}

func TestTransportStepCreatesPoolExportService(t *testing.T) {
	f := newFixture(t, withPoolExport(), withObjects(healthyObjects()...))
	f.mustReconcile()
	svc, err := f.poolService()
	require.NoError(t, err)
	assert.True(t, metav1.IsControlledBy(svc, f.endpoint()))
	assert.Equal(t, "pylon", svc.Labels["gds.nvidia.com/role"])
	assert.Equal(t, "llama", svc.Labels["gds.nvidia.com/pool"])
	assert.Equal(t, testGPU, svc.Annotations["gds.nvidia.com/hardware-class"], "the GPU step runs first")
	assert.Equal(t, testModel, svc.Annotations["gds.nvidia.com/pool-models"])
	assert.Contains(t, f.deployment().Spec.Template.Spec.Containers[0].Args, "--gds-pool-name=llama")

	without := newFixture(t, withObjects(healthyObjects()...))
	without.mustReconcile()
	_, err = without.poolService()
	assert.True(t, apierrors.IsNotFound(err), "no Service without pool export")
}

// TestTransportStepServiceProblemEvent checks that a Service conflict emits
// one Warning Event, leaves the conditions to the registration observer, and
// warns again only after it was resolved and came back.
func TestTransportStepServiceProblemEvent(t *testing.T) {
	foreign := service(testNamespace, "pylon-llama")
	f := newFixture(t, withPoolExport(), withObjects(append(healthyObjects(), foreign)...))
	warning := `Warning TransportObjectConflict Service "pylon-llama" exists and is not controlled by this InferenceEndpoint; the transport is not exported to the Global Discovery Service`

	f.mustReconcile()
	assert.Contains(t, f.events(), warning)
	assert.Equal(t, "TunnelNotConnected", f.condition(pylonv1alpha1.ConditionTransportReady).Reason, "no fast negative")

	f.mustReconcile()
	assert.NotContains(t, f.events(), warning, "not repeated while it lasts")

	// Resolved: the operator creates its Service.
	require.NoError(t, f.client.Delete(context.Background(), foreign))
	f.mustReconcile()
	svc, err := f.poolService()
	require.NoError(t, err)
	assert.NotContains(t, f.events(), warning)

	// Back again: a new Warning.
	require.NoError(t, f.client.Delete(context.Background(), svc))
	require.NoError(t, f.client.Create(context.Background(), service(testNamespace, "pylon-llama")))
	f.mustReconcile()
	assert.Contains(t, f.events(), warning)
}

// TestTransportStepServiceProblemEventAfterFailedPatch checks that a Warning
// lost with a failed status patch is emitted by the next reconcile.
func TestTransportStepServiceProblemEventAfterFailedPatch(t *testing.T) {
	var failPatch atomic.Bool
	failPatch.Store(true)
	f := newFixture(t, withPoolExport(), withObjects(append(healthyObjects(), service(testNamespace, "pylon-llama"))...),
		withInterceptors(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if failPatch.Load() {
					return fmt.Errorf("apiserver says no")
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}))
	_, err := f.reconcile()
	require.Error(t, err)
	assert.Empty(t, f.events(), "no Event without a status patch")

	failPatch.Store(false)
	f.mustReconcile()
	assert.Contains(t, f.events(), `Warning TransportObjectConflict Service "pylon-llama" exists and is not controlled by this InferenceEndpoint; the transport is not exported to the Global Discovery Service`)
}

func TestTransportStepForget(t *testing.T) {
	step := &transportStep{}
	rc := newReconcileContext(inferenceEndpoint(1), testConfig())
	problem := &transport.Problem{EventReason: transport.EventReasonTransportObjectConflict, Message: "conflict"}
	step.noteServiceProblem(rc, problem)
	for _, f := range rc.afterEvents {
		f()
	}
	require.Len(t, rc.events, 1)

	rc = newReconcileContext(inferenceEndpoint(1), testConfig())
	step.noteServiceProblem(rc, problem)
	assert.Empty(t, rc.events, "remembered")

	step.Forget(testKey)
	step.noteServiceProblem(rc, problem)
	assert.Len(t, rc.events, 1, "forgotten with the endpoint")
}

func TestTransportStepOwnsServices(t *testing.T) {
	var kinds []string
	for _, obj := range ownedTypes() {
		kinds = append(kinds, fmt.Sprintf("%T", obj))
	}
	assert.Equal(t, []string{"*v1.Deployment", "*v1.Service"}, kinds)
}

func TestTransportStepSetupWithManager(t *testing.T) {
	for _, trustBundle := range []string{"", "router-ca"} {
		cfg := testConfig()
		cfg.TrustBundleConfigMap = trustBundle
		step := &transportStep{reconciler: transport.New(nil, cfg)}
		var ms ManagerSetup = step
		assert.NoError(t, ms.SetupWithManager(context.Background(), nil, builder.ControllerManagedBy(nil)), trustBundle)
	}
}

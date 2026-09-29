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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

const (
	testPod        = "pylon-llama-7c9d4-xk2p"
	scrapeInterval = 5 * time.Second

	steadyPylon = `# TYPE pylon_registration_stream_connected gauge
pylon_registration_stream_connected{router="router-0"} 1
# TYPE pylon_reverse_tunnel_connected gauge
pylon_reverse_tunnel_connected{router="router-0"} 1
`
	rejectedPylon = steadyPylon + `# TYPE pylon_registration_stream_closures_total counter
pylon_registration_stream_closures_total{router="router-1",reason="unauthenticated"} 1
`
)

// transportPod is a running, ready transport pod of the test endpoint.
func transportPod(name string) *corev1.Pod {
	labels := transport.PodLabels(inferenceEndpoint(1))
	labels[config.ManagedByLabel] = config.ManagedBy
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.0.0.2",
			StartTime: &metav1.Time{Time: time.Now().Add(-time.Minute)},
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute)),
			}},
		},
	}
}

func (f *fixture) registeredGauge(endpoint string) (float64, bool) {
	f.t.Helper()
	families, err := f.registry.Gather()
	require.NoError(f.t, err)
	for _, fam := range families {
		if fam.GetName() != "nvcf_pylon_operator_registered" {
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

func TestRegistrationObserverSteadyState(t *testing.T) {
	var patches atomic.Int32
	f := newFixture(t,
		withObjects(append(healthyObjects(), transportPod(testPod))...),
		withConfig(func(c *config.Config) { c.ScrapeInterval = scrapeInterval }),
		withInterceptors(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patches.Add(1)
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}))
	f.pylon.set(steadyPylon)

	res := f.mustReconcile()
	assert.GreaterOrEqual(t, res.RequeueAfter, scrapeInterval*9/10)
	assert.Less(t, res.RequeueAfter, scrapeInterval*11/10)

	tr := f.condition(pylonv1alpha1.ConditionTransportReady)
	assert.Equal(t, metav1.ConditionTrue, tr.Status)
	assert.Equal(t, "PylonConnected", tr.Reason)
	reg := f.condition(pylonv1alpha1.ConditionRegistered)
	assert.Equal(t, metav1.ConditionTrue, reg.Status)
	assert.Equal(t, "RegisteredWithRouter", reg.Reason)
	assert.Equal(t, `1 of 1 transport pods have a registration stream open to 1 router ("router-0")`, reg.Message)

	ep := f.endpoint()
	require.NotNil(t, ep.Status.Registration)
	assert.Equal(t, "spark-berlin", ep.Status.Registration.ClusterID)
	assert.Equal(t, int32(1), ep.Status.Registration.RoutersConnected)
	require.NotNil(t, ep.Status.Registration.LastRegisteredTime)
	assert.WithinDuration(t, time.Now(), ep.Status.Registration.LastRegisteredTime.Time, 5*time.Second)
	assert.Equal(t, []pylonv1alpha1.ServerStatus{{
		InferenceServerID:   "spark-berlin.models.llama." + testPod,
		Pod:                 testPod,
		RegistrationStreams: 1,
		ReverseTunnels:      1,
	}}, ep.Status.Servers)

	events := f.events()
	assert.Contains(t, events, `Normal PylonConnected TransportReady is True: 1 of 1 transport pods have a reverse tunnel connected to 1 router ("router-0")`)
	assert.Contains(t, events, `Normal RegisteredWithRouter Registered is True: 1 of 1 transport pods have a registration stream open to 1 router ("router-0")`)
	gauge, ok := f.registeredGauge("models/llama")
	require.True(t, ok)
	assert.Equal(t, 1.0, gauge)
	assert.Equal(t, int32(1), patches.Load())

	// Nothing changed: no status write and no Events.
	f.mustReconcile()
	assert.Equal(t, int32(1), patches.Load())
	assert.Empty(t, f.events())

	// Another router rejects this cluster while router-0 keeps the stream:
	// Registered stays True and a Warning names the rejection.
	f.pylon.set(rejectedPylon)
	f.mustReconcile()
	assert.Equal(t, metav1.ConditionTrue, f.condition(pylonv1alpha1.ConditionRegistered).Status)
	events = f.events()
	require.Len(t, events, 1)
	assert.True(t, strings.HasPrefix(events[0],
		`Warning RegistrationStreamRejected Registration rejected since the last scrape: router "router-1" closed the stream of pod `+testPod+` with unauthenticated (1 new)`), events[0])

	// The endpoint goes away: its registered series goes with it.
	require.NoError(t, f.client.Delete(context.Background(), f.endpoint()))
	f.mustReconcile()
	_, ok = f.registeredGauge("models/llama")
	assert.False(t, ok)
}

func TestRegistrationObserverRouterUnreachable(t *testing.T) {
	f := newFixture(t, withObjects(append(healthyObjects(), transportPod(testPod))...))
	f.pylon.set("# no registration series yet\n")
	f.mustReconcile()
	reg := f.condition(pylonv1alpha1.ConditionRegistered)
	assert.Equal(t, metav1.ConditionFalse, reg.Status)
	assert.Equal(t, "RouterUnreachable", reg.Reason)
	assert.Equal(t, "TunnelNotConnected", f.condition(pylonv1alpha1.ConditionTransportReady).Reason)
	assert.Contains(t, f.events(), "Warning RouterUnreachable Registered is False: "+reg.Message)
	gauge, _ := f.registeredGauge("models/llama")
	assert.Zero(t, gauge)
}

func TestRegistrationStepFastNegativeReportsNoRouters(t *testing.T) {
	ep := inferenceEndpoint(1)
	last := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	ep.Status.Registration = &pylonv1alpha1.RegistrationStatus{ClusterID: "spark-berlin", RoutersConnected: 2, LastRegisteredTime: &last}
	ep.Status.Servers = []pylonv1alpha1.ServerStatus{{InferenceServerID: "spark-berlin.models.llama.p", Pod: "p", RegistrationStreams: 2}}
	rc := newReconcileContext(ep, testConfig())
	rc.Transport = &transport.State{DeploymentName: "pylon-llama", ScaledToZero: true}

	step := &registrationStep{observer: registration.New(nil, nil)}
	require.NoError(t, step.Run(context.Background(), rc))
	assert.Nil(t, rc.Condition(pylonv1alpha1.ConditionTransportReady), "left to the transport step")
	assert.Nil(t, rc.Condition(pylonv1alpha1.ConditionRegistered), "left to the transport step")
	assert.Equal(t, &pylonv1alpha1.RegistrationStatus{ClusterID: "spark-berlin", LastRegisteredTime: &last}, ep.Status.Registration)
	assert.Nil(t, ep.Status.Servers)
	assert.Equal(t, probeInterval, rc.requeueAfter, "no scrape requeue")
	assert.Empty(t, rc.events)
}

func TestRegistrationStepListError(t *testing.T) {
	f := newFixture(t, withObjects(healthyObjects()...), withInterceptors(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				return fmt.Errorf("pod cache not synced")
			}
			return c.List(ctx, list, opts...)
		},
	}))
	_, err := f.reconcile()
	require.EqualError(t, err, "step registration-observer: listing transport pods: pod cache not synced")
	assert.Equal(t, int64(0), f.endpoint().Status.ObservedGeneration)
}

// forgetter records the keys the reconciler forgets.
type forgetter struct {
	stepFunc
	keys []types.NamespacedName
}

func (f *forgetter) Forget(key types.NamespacedName) { f.keys = append(f.keys, key) }

func TestReconcileForgetsDeletedEndpoints(t *testing.T) {
	noop := stepFunc{name: "noop", run: func(context.Context, *ReconcileContext) error { return nil }}

	gone := &forgetter{stepFunc: noop}
	f := newFixture(t, withSteps(gone))
	assert.Equal(t, reconcile.Result{}, f.mustReconcile())
	assert.Equal(t, []types.NamespacedName{testKey}, gone.keys, "endpoint not found")

	vanished := &forgetter{stepFunc: stepFunc{name: "set", run: func(_ context.Context, rc *ReconcileContext) error {
		rc.SetRegistered(metav1.ConditionFalse, pylonv1alpha1.RegisteredReasonPending, "x")
		return nil
	}}}
	f = newFixture(t, withObjects(inferenceEndpoint(1)), withSteps(vanished), withInterceptors(interceptor.Funcs{
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return notFound()
		},
	}))
	f.mustReconcile()
	assert.Equal(t, []types.NamespacedName{testKey}, vanished.keys, "endpoint deleted before the status patch")
}

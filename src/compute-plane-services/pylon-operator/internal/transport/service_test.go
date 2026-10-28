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

package transport

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

// poolExportConfig is gdsConfig with --gds-pool-export.
func poolExportConfig() config.Config {
	cfg := gdsConfig()
	cfg.GDSPoolExport = true
	return cfg
}

func (f *fixture) service(ep *pylonv1alpha1.InferenceEndpoint) *corev1.Service {
	f.t.Helper()
	s := &corev1.Service{}
	require.NoError(f.t, f.client.Get(context.Background(), client.ObjectKey{Namespace: ep.Namespace, Name: ServiceName(ep)}, s))
	return s
}

func (f *fixture) noService(ep *pylonv1alpha1.InferenceEndpoint) {
	f.t.Helper()
	err := f.client.Get(context.Background(), client.ObjectKey{Namespace: ep.Namespace, Name: ServiceName(ep)}, &corev1.Service{})
	assert.True(f.t, apierrors.IsNotFound(err), "Service must not exist: %v", err)
}

func withGPU(ep *pylonv1alpha1.InferenceEndpoint, product string) *pylonv1alpha1.InferenceEndpoint {
	ep.Status.GPU = &pylonv1alpha1.GPUStatus{Product: product, Source: pylonv1alpha1.GPUSourceNodeLabels}
	return ep
}

func TestReconcileCreatesPoolExportService(t *testing.T) {
	f := newFixture(t, poolExportConfig(), credential(testToken))
	ep := withGPU(endpoint(), "NVIDIA-GB10")

	st := f.reconcile(ep)
	assert.Nil(t, st.Problem)
	assert.Nil(t, st.ServiceProblem)
	assert.Equal(t, []string{
		"create Secret models/pylon-operator-cluster-credential",
		"create Deployment models/pylon-llama",
		"create Service models/pylon-llama",
	}, f.takeWrites(), "the Service follows the Deployment")

	want := Service(ep)
	got := f.service(ep)
	assert.Equal(t, want.Labels, got.Labels)
	assert.Equal(t, want.Annotations, got.Annotations)
	assert.Equal(t, want.OwnerReferences, got.OwnerReferences)
	assert.True(t, metav1.IsControlledBy(got, ep), "controlled by the endpoint")
	assert.Equal(t, want.Spec, got.Spec)
	assert.Equal(t, "NVIDIA-GB10", got.Annotations["gds.nvidia.com/hardware-class"])
	assert.Contains(t, f.deployment(ep).Spec.Template.Spec.Containers[0].Args, "--gds-pool-name=llama")

	// Fields the API server fills in are not drift.
	got.Spec.ClusterIP = "10.96.0.12"
	got.Spec.ClusterIPs = []string{"10.96.0.12"}
	got.Spec.SessionAffinity = corev1.ServiceAffinityNone
	got.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	got.Spec.InternalTrafficPolicy = ptr.To(corev1.ServiceInternalTrafficPolicyCluster)
	require.NoError(t, f.client.Update(context.Background(), got))
	f.takeWrites()
	for range 3 {
		f.reconcile(ep)
	}
	assert.Empty(t, f.takeWrites(), "nothing changed, nothing written")
}

func TestReconcileCreatesPoolExportServiceWhenScaledToZero(t *testing.T) {
	f := newFixture(t, poolExportConfig(), credential(testToken))
	ep := endpoint()
	setReady(ep, metav1.ConditionFalse, pylonv1alpha1.ReadyReasonModelNameMismatch)
	st := f.reconcile(ep)
	assert.True(t, st.ScaledToZero)
	f.service(ep)
}

func TestReconcileWithoutPoolExportWritesNoService(t *testing.T) {
	staticWithFlag := testConfig()
	staticWithFlag.GDSPoolExport = true
	for name, cfg := range map[string]config.Config{
		"static":                   testConfig(),
		"gds":                      gdsConfig(),
		"flag without gds ignored": staticWithFlag,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, cfg, credential(testToken))
			ep := endpoint()
			st := f.reconcile(ep)
			assert.Nil(t, st.ServiceProblem)
			for _, w := range f.takeWrites() {
				assert.NotContains(t, w, "Service")
			}
			f.noService(ep)
			assert.NotContains(t, fmt.Sprint(f.deployment(ep).Spec.Template.Spec.Containers[0].Args), "--gds-pool-name")
		})
	}

	t.Run("a Service created earlier is left alone", func(t *testing.T) {
		ep := endpoint()
		earlier := Service(ep)
		earlier.Labels["gds.nvidia.com/pool"] = "old-pool"
		f := newFixture(t, gdsConfig(), credential(testToken), earlier)
		f.reconcile(ep)
		for _, w := range f.takeWrites() {
			assert.NotContains(t, w, "Service", "neither updated nor deleted")
		}
		assert.Equal(t, "old-pool", f.service(ep).Labels["gds.nvidia.com/pool"])
	})
}

func TestReconcileRestoresPoolExportService(t *testing.T) {
	f := newFixture(t, poolExportConfig(), credential(testToken))
	ep := withGPU(endpoint(), "NVIDIA-GB10")
	f.reconcile(ep)

	s := f.service(ep)
	s.Labels["gds.nvidia.com/pool"] = "other"
	delete(s.Labels, "gds.nvidia.com/export")
	s.Labels["team"] = "a"
	s.Annotations["gds.nvidia.com/pool-models"] = "other/model"
	delete(s.Annotations, "gds.nvidia.com/hardware-class")
	s.Annotations["gds.nvidia.com/dc"] = "us-east-1"
	s.Spec.Type = corev1.ServiceTypeNodePort
	s.Spec.Selector["extra"] = "x"
	s.Spec.Ports = []corev1.ServicePort{
		{Name: "metrics", Protocol: corev1.ProtocolTCP, Port: 9090, TargetPort: intstr.FromInt32(9089), NodePort: 30089},
		{Name: "debug", Protocol: corev1.ProtocolTCP, Port: 6060},
	}
	s.Spec.ClusterIP = "10.96.0.12"
	require.NoError(t, f.client.Update(context.Background(), s))
	f.takeWrites()

	f.reconcile(ep)
	assert.Equal(t, []string{"patch Service models/pylon-llama"}, f.takeWrites(), "one patch, the Deployment is untouched")
	want := Service(ep)
	got := f.service(ep)
	wantLabels := map[string]string{"team": "a"}
	for k, v := range want.Labels {
		wantLabels[k] = v
	}
	assert.Equal(t, wantLabels, got.Labels, "foreign labels stay")
	assert.Equal(t, map[string]string{
		"gds.nvidia.com/pool-models":    testModel,
		"gds.nvidia.com/hardware-class": "NVIDIA-GB10",
		"gds.nvidia.com/dc":             "us-east-1",
	}, got.Annotations, "foreign annotations stay")
	assert.Equal(t, corev1.ServiceTypeClusterIP, got.Spec.Type)
	assert.Equal(t, want.Spec.Selector, got.Spec.Selector)
	assert.Equal(t, want.Spec.Ports, got.Spec.Ports)
	assert.Equal(t, "10.96.0.12", got.Spec.ClusterIP, "the cluster IP is not the operator's")
	assert.True(t, metav1.IsControlledBy(got, ep))
	assert.Contains(t, f.logs.String(), "Updated the pool export Service")

	f.reconcile(ep)
	assert.Empty(t, f.takeWrites(), "restored")

	// Labels and annotations removed altogether come back.
	s = f.service(ep)
	s.Labels, s.Annotations = nil, nil
	require.NoError(t, f.client.Update(context.Background(), s))
	f.takeWrites()
	f.reconcile(ep)
	assert.Equal(t, []string{"patch Service models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, want.Labels, f.service(ep).Labels)
	assert.Equal(t, want.Annotations, f.service(ep).Annotations)
}

func TestReconcilePoolExportServiceFollowsEndpoint(t *testing.T) {
	f := newFixture(t, poolExportConfig(), credential(testToken))
	ep := endpoint()
	f.reconcile(ep)
	f.setReadyReplicas(ep, 1)
	hash := f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation]
	assert.NotContains(t, f.service(ep).Annotations, "gds.nvidia.com/hardware-class", "no hardware class while the GPU is unknown")
	f.takeWrites()

	// The GPU product reaches the Service only.
	for _, product := range []string{"NVIDIA-GB10", "NVIDIA-GB300"} {
		withGPU(ep, product)
		f.reconcile(ep)
		assert.Equal(t, []string{"patch Service models/pylon-llama"}, f.takeWrites(), product)
		assert.Equal(t, product, f.service(ep).Annotations["gds.nvidia.com/hardware-class"], product)
		assert.Equal(t, hash, f.deployment(ep).Spec.Template.Annotations[SpecHashAnnotation], "a GPU type change does not roll the transport")
	}

	// An unknown GPU removes the hardware class.
	ep.Status.GPU = &pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown}
	f.reconcile(ep)
	assert.Equal(t, []string{"patch Service models/pylon-llama"}, f.takeWrites())
	assert.NotContains(t, f.service(ep).Annotations, "gds.nvidia.com/hardware-class")

	// The model name reaches both.
	ep.Spec.ModelName = "mistral/mistral-7b"
	f.reconcile(ep)
	assert.Equal(t, []string{"patch Deployment models/pylon-llama", "patch Service models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, "mistral/mistral-7b", f.service(ep).Annotations["gds.nvidia.com/pool-models"])

	// A pool name in the spec renames the pool and the Pylon agent.
	ep.Spec.GlobalDiscovery = &pylonv1alpha1.GlobalDiscoverySpec{PoolName: "mistral-pool"}
	f.reconcile(ep)
	assert.Equal(t, []string{"patch Deployment models/pylon-llama", "patch Service models/pylon-llama"}, f.takeWrites())
	assert.Equal(t, "mistral-pool", f.service(ep).Labels["gds.nvidia.com/pool"])
	assert.Contains(t, f.deployment(ep).Spec.Template.Spec.Containers[0].Args, "--gds-pool-name=mistral-pool")
}

func TestReconcilePoolExportServiceConflict(t *testing.T) {
	ep := endpoint()
	for name, foreign := range map[string]*corev1.Service{
		"no owner": {
			ObjectMeta: metav1.ObjectMeta{Name: "pylon-llama", Namespace: testNamespace, Labels: map[string]string{"app": "mine"}},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
		},
		"another controller": func() *corev1.Service {
			s := Service(ep)
			s.OwnerReferences[0].UID = "someone-else"
			s.Labels = map[string]string{"app": "mine"}
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, poolExportConfig(), credential(testToken), foreign)
			st := f.reconcile(ep)
			assert.Nil(t, st.Problem, "the transport is not blocked")
			require.NotNil(t, st.ServiceProblem)
			assert.Equal(t, Problem{
				EventReason: EventReasonTransportObjectConflict,
				Message:     `Service "pylon-llama" exists and is not controlled by this InferenceEndpoint; the transport is not exported to the Global Discovery Service`,
			}, *st.ServiceProblem)
			assert.Equal(t, []string{
				"create Secret models/pylon-operator-cluster-credential",
				"create Deployment models/pylon-llama",
			}, f.takeWrites(), "the foreign Service is left alone")
			assert.Equal(t, map[string]string{"app": "mine"}, f.service(ep).Labels)
			verdict, ok := st.FastNegative()
			require.True(t, ok)
			assert.Equal(t, `Transport Deployment "pylon-llama" has no ready pods (0 of 1)`, verdict.Message, "the Service problem is no fast negative")
		})
	}
}

func TestReconcilePoolExportServiceWaitsForDeployment(t *testing.T) {
	t.Run("missing credential", func(t *testing.T) {
		f := newFixture(t, poolExportConfig())
		ep := endpoint()
		st := f.reconcile(ep)
		require.NotNil(t, st.Problem)
		assert.Nil(t, st.ServiceProblem)
		f.noService(ep)
	})

	t.Run("foreign Deployment", func(t *testing.T) {
		foreign := Deployment(endpoint(), poolExportConfig(), 1)
		foreign.OwnerReferences[0].UID = "someone-else"
		f := newFixture(t, poolExportConfig(), credential(testToken), foreign)
		ep := endpoint()
		st := f.reconcile(ep)
		require.NotNil(t, st.Problem)
		f.noService(ep)
	})
}

func TestReconcilePoolExportServiceErrors(t *testing.T) {
	boom := fmt.Errorf("apiserver says no")
	tests := []struct {
		name    string
		op      string
		err     error
		setup   func(*fixture, *pylonv1alpha1.InferenceEndpoint)
		wantErr string
	}{
		{name: "read Service", op: "get", err: boom, wantErr: `reading Service "pylon-llama": apiserver says no`},
		{name: "create Service", op: "create", err: boom, wantErr: `creating Service "pylon-llama": apiserver says no`},
		{
			name: "Service not yet in the cache", op: "create",
			err:     apierrors.NewAlreadyExists(schema.GroupResource{Resource: "services"}, "pylon-llama"),
			wantErr: `creating Service "pylon-llama": services "pylon-llama" already exists`,
		},
		{
			name: "patch Service", op: "patch", err: boom,
			setup: func(f *fixture, ep *pylonv1alpha1.InferenceEndpoint) {
				f.reconcile(ep)
				withGPU(ep, "NVIDIA-GB10")
			},
			wantErr: `updating Service "pylon-llama": apiserver says no`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, poolExportConfig(), credential(testToken))
			ep := endpoint()
			if tt.setup != nil {
				tt.setup(f, ep)
			}
			f.fail = func(op, kind, _ string) error {
				if op == tt.op && kind == "Service" {
					return tt.err
				}
				return nil
			}
			st, err := f.r.Reconcile(f.ctx(), ep)
			require.EqualError(t, err, tt.wantErr)
			assert.Equal(t, "pylon-llama", st.DeploymentName)
			f.deployment(ep)
		})
	}
}

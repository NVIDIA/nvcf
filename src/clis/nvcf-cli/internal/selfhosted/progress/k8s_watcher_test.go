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

package progress

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// recordingSink captures every emitted Event for later assertion.
type recordingSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *recordingSink) Emit(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (*recordingSink) Close() error { return nil }

func (s *recordingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Event, len(s.events))
	copy(cp, s.events)
	return cp
}

// lastByResource returns the latest PhaseProgress for each resource type.
func (s *recordingSink) lastByResource() map[string]PhaseProgress {
	out := map[string]PhaseProgress{}
	for _, e := range s.snapshot() {
		if pp, ok := e.(PhaseProgress); ok {
			out[pp.Resource] = pp
		}
	}
	return out
}

func TestWatchResources_EmitsCountsForEachType(t *testing.T) {
	nsList := []string{"sis", "nvcf", "cassandra-system"}

	objs := []runtime.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sis"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "cassandra-system"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}, // not Active → not done
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "sis", Namespace: "sis"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(2)},
			Status:     appsv1.DeploymentStatus{Replicas: 2, AvailableReplicas: 2},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api", Namespace: "nvcf"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(1)},
			Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 0}, // not done
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "cassandra", Namespace: "cassandra-system"},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptrInt32(3)},
			Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "cassandra-init", Namespace: "cassandra-system"},
			Status:     batchv1.JobStatus{Succeeded: 1},
		},
	}
	crdObjs := []runtime.Object{
		&apiextv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcfbackends.nvcf.nvidia.com"},
			Status: apiextv1.CustomResourceDefinitionStatus{
				Conditions: []apiextv1.CustomResourceDefinitionCondition{
					{Type: apiextv1.Established, Status: apiextv1.ConditionTrue},
				},
			},
		},
	}

	kubeClient := fake.NewSimpleClientset(objs...)
	apiextClient := apiextfake.NewSimpleClientset(crdObjs...)
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WatchResources(ctx, kubeClient, apiextClient, nsList, 4, "apply-cp", sink)
	}()

	// Give informers + debounce a moment to settle.
	require.Eventually(t, func() bool {
		last := sink.lastByResource()
		return len(last) == 5
	}, 3*time.Second, 50*time.Millisecond, "did not see all 5 resource types")

	last := sink.lastByResource()
	assert.Equal(t, 4, last["namespaces"].Num)        // phaseNum baked in
	assert.Equal(t, "apply-cp", last["namespaces"].Name) // phaseName baked in
	assert.Equal(t, "namespaces", last["namespaces"].Resource)
	assert.Equal(t, 2, last["namespaces"].Done)
	assert.Equal(t, 3, last["namespaces"].Total)

	assert.Equal(t, "crds", last["crds"].Resource)
	assert.Equal(t, 1, last["crds"].Done)
	assert.Equal(t, 1, last["crds"].Total)

	assert.Equal(t, "deployments", last["deployments"].Resource)
	assert.Equal(t, 1, last["deployments"].Done)
	assert.Equal(t, 2, last["deployments"].Total)

	assert.Equal(t, "statefulsets", last["statefulsets"].Resource)
	assert.Equal(t, 1, last["statefulsets"].Done)
	assert.Equal(t, 1, last["statefulsets"].Total)

	assert.Equal(t, "jobs", last["jobs"].Resource)
	assert.Equal(t, 1, last["jobs"].Done)
	assert.Equal(t, 1, last["jobs"].Total)

	cancel()
	err := <-done
	assert.ErrorIs(t, err, context.Canceled)
}

func TestWatchResources_FiltersOutOfNamespace(t *testing.T) {
	nsList := []string{"sis"}
	inScope := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "sis", Namespace: "sis"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(1)},
		Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1},
	}
	outOfScope := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(2)},
		Status:     appsv1.DeploymentStatus{Replicas: 2, AvailableReplicas: 2},
	}

	kubeClient := fake.NewSimpleClientset(inScope, outOfScope)
	apiextClient := apiextfake.NewSimpleClientset()
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WatchResources(ctx, kubeClient, apiextClient, nsList, 4, "apply-cp", sink)
	}()

	require.Eventually(t, func() bool {
		last := sink.lastByResource()
		return last["deployments"].Total == 1
	}, 3*time.Second, 50*time.Millisecond, "did not see filtered Deployment count")

	last := sink.lastByResource()
	assert.Equal(t, 1, last["deployments"].Done)
	assert.Equal(t, 1, last["deployments"].Total) // kube-system filtered out

	cancel()
	<-done
}

func TestWatchResources_DebouncesEventStorms(t *testing.T) {
	// Construct fake clients with no initial objects, start the watcher,
	// then add a deployment. Confirm we see exactly ONE PhaseProgress for
	// deployments per state change, not one per informer event.
	kubeClient := fake.NewSimpleClientset()
	apiextClient := apiextfake.NewSimpleClientset()
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- WatchResources(ctx, kubeClient, apiextClient, []string{"sis"}, 4, "apply-cp", sink)
	}()

	// Wait for initial sync to settle (5 zero-state events).
	require.Eventually(t, func() bool {
		return len(sink.lastByResource()) == 5
	}, 3*time.Second, 50*time.Millisecond)

	initial := len(sink.snapshot())

	// Rapidly add 5 deployments; after debounce settles we should see
	// strictly fewer than 5 deployment events total.
	for i := 0; i < 5; i++ {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("dep-%d", i), Namespace: "sis"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(1)},
			Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1},
		}
		_, err := kubeClient.AppsV1().Deployments("sis").Create(ctx, d, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Wait for debounce + final emit (500ms debounce + 300ms safety margin).
	time.Sleep(800 * time.Millisecond)

	afterAdds := sink.snapshot()
	deploymentEvents := 0
	for _, e := range afterAdds[initial:] {
		if pp, ok := e.(PhaseProgress); ok && pp.Resource == "deployments" {
			deploymentEvents++
		}
	}
	// Without debouncing this would be ~5; with 500ms debounce we expect 1-2.
	assert.LessOrEqual(t, deploymentEvents, 2, "debounce should collapse event storm; got %d events", deploymentEvents)
}

func TestWatchResources_StopsOnContextCancel(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()
	apiextClient := apiextfake.NewSimpleClientset()
	sink := &recordingSink{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WatchResources(ctx, kubeClient, apiextClient, []string{"sis"}, 4, "apply-cp", sink)
	}()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("WatchResources did not exit within 2s of ctx cancel")
	}
}

// ptrInt32 returns a pointer to v. Convenience for *int32 fields.
func ptrInt32(v int32) *int32 { return &v }

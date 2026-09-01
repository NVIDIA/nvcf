/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nvca

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	fakebartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func durableModelCacheBindingRequest(
	t *testing.T,
	workflow nvcastorage.ModelCacheWorkflow,
) (*nvcav2beta1.ICMSRequest, *nvcastorage.PersistedModelCacheStorageSelection) {
	t.Helper()
	req := selectionRequest(workflow == nvcastorage.ModelCacheWorkflowHelm)
	req.UID = types.UID("request-uid")
	req.Spec.NCAId = "nca-a"
	req.Finalizers = []string{NVCAFinalizer}
	selection, err := nvcastorage.NewPersistedModelCacheStorageSelection(
		workflow,
		nvcastorage.ModelCacheSelectionDurable,
		resolvedSelectionForSetup(t),
	)
	require.NoError(t, err)
	raw, err := selection.Marshal()
	require.NoError(t, err)
	req.Annotations = map[string]string{
		nvcastorage.ModelCacheStorageSelectionAnnotationKey: raw,
	}
	return req, selection
}

func bindingTestBackend(
	req *nvcav2beta1.ICMSRequest,
	bartObjects ...runtime.Object,
) (*BackendK8sCache, *fakebartclient.Clientset, *fakek8sclient.Clientset) {
	objects := []runtime.Object{req.DeepCopy()}
	objects = append(objects, bartObjects...)
	bart := fakebartclient.NewSimpleClientset(objects...)
	k8s := fakek8sclient.NewSimpleClientset(
		selectionStorageClass(),
		selectionCatalogConfigMap(selectionCatalogNVMesh),
	)
	return &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			BART: bart,
			K8s:  k8s,
		},
		podInstanceNamespace: "pod-instances",
		systemNamespace:      selectionCatalogNamespace,
	}, bart, k8s
}

func assignCreatedBindingUID(bart *fakebartclient.Clientset, uid types.UID) {
	bart.PrependReactor("create", "modelcachebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		created := action.(k8stesting.CreateAction).GetObject().(*nvcav2beta1.ModelCacheBinding)
		created.UID = uid
		return false, nil, nil
	})
}

func boundSelectionPayload(
	t *testing.T,
	selection *nvcastorage.PersistedModelCacheStorageSelection,
	binding *nvcav2beta1.ModelCacheBinding,
) string {
	t.Helper()
	bound := *selection
	bound.BindingName = binding.Name
	bound.BindingUID = binding.UID
	raw, err := bound.Marshal()
	require.NoError(t, err)
	return raw
}

func boundModelCacheBindingFixture(
	t *testing.T,
	workflow nvcastorage.ModelCacheWorkflow,
	phase nvcav2beta1.ModelCacheBindingPhase,
	refs []nvcav2beta1.ModelCacheBindingRequestReference,
) (*nvcav2beta1.ICMSRequest, *nvcav2beta1.ModelCacheBinding, *BackendK8sCache, *fakebartclient.Clientset) {
	t.Helper()
	req, selection := durableModelCacheBindingRequest(t, workflow)
	writerNamespace := "pod-instances"
	if workflow == nvcastorage.ModelCacheWorkflowHelm {
		writerNamespace = nvcastorage.ModelCacheInitNamespace
	}
	binding, err := nvcastorage.NewModelCacheBinding(
		selection, req.Spec.NCAId, "model-cache-handle", writerNamespace)
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.ResourceVersion = "1"
	binding.Status.Phase = phase
	for i := range refs {
		if refs[i].Namespace == "requests" {
			refs[i].Namespace = req.Namespace
		}
	}
	binding.Status.RequestReferences = append(
		[]nvcav2beta1.ModelCacheBindingRequestReference(nil), refs...)
	req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] =
		boundSelectionPayload(t, selection, binding)
	backend, bart, _ := bindingTestBackend(req, binding)
	return req, binding, backend, bart
}

func countModelCacheBindingStatusUpdates(actions []k8stesting.Action) int {
	updates := 0
	for _, action := range actions {
		if action.GetVerb() == "update" && action.GetResource().Resource == "modelcachebindings" &&
			action.GetSubresource() == "status" {
			updates++
		}
	}
	return updates
}

func installActiveRegularModelCacheBinding(
	t *testing.T,
	backend *K8sComputeBackend,
	req *nvcav2beta1.ICMSRequest,
	rawSelection string,
) *nvcav2beta1.ModelCacheBinding {
	t.Helper()
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(rawSelection)
	require.NoError(t, err)
	require.Equal(t, nvcastorage.ModelCacheWorkflowRegular, selection.Workflow)
	require.Equal(t, nvcastorage.ModelCacheSelectionDurable, selection.Mode)

	template := selectionRequest(false)
	req.Spec.CreationMsgInfo = template.Spec.CreationMsgInfo
	req.Spec.NCAId = "nca-a"
	req.UID = types.UID("request-uid")
	binding, err := nvcastorage.NewModelCacheBinding(
		selection,
		req.Spec.NCAId,
		req.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification.CacheHandle,
		backend.bk8s.podInstanceNamespace,
	)
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
		Namespace: req.Namespace,
		Name:      req.Name,
		UID:       req.UID,
	}}
	if req.Annotations == nil {
		req.Annotations = map[string]string{}
	}
	req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] =
		boundSelectionPayload(t, selection, binding)
	backend.clients.BART = fakebartclient.NewSimpleClientset(req.DeepCopy(), binding.DeepCopy())
	return binding
}

func TestEnsureModelCacheBindingCreatesBeforeRuntimeAndSurvivesLiveDrift(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, k8s := bindingTestBackend(req)
	assignCreatedBindingUID(bart, types.UID("binding-uid"))

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed)

	persisted, err := bart.NvcaV2beta1().ICMSRequests(req.Namespace).
		Get(t.Context(), req.Name, metav1.GetOptions{})
	require.NoError(t, err)
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(
		persisted.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey])
	require.NoError(t, err)
	assert.Equal(t, types.UID("binding-uid"), selection.BindingUID)
	assert.Equal(t, nvcastorage.ModelCacheBindingName("model-cache-handle"), selection.BindingName)

	binding, err := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		Get(t.Context(), selection.BindingName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, binding.Status.Phase)
	assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
		binding, req.Namespace, req.Name, req.UID))

	// Once committed, the immutable binding is authoritative. A live class
	// deletion must not cause reselection or block adoption on a later reconcile.
	require.NoError(t, k8s.StorageV1().StorageClasses().Delete(
		t.Context(), nvcastorage.DefaultModelCacheStorageClassName, metav1.DeleteOptions{}))
	k8s.ClearActions()
	changed, err = backend.ensureModelCacheBinding(t.Context(), persisted.DeepCopy())
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, k8s.Actions(), "a committed binding must not re-read the live StorageClass")
}

func TestEnsureModelCacheBindingRejectsPreCreateStorageClassDrift(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, k8s := bindingTestBackend(req)
	drifted := selectionStorageClass()
	drifted.Parameters["pool"] = "changed"
	k8s = fakek8sclient.NewSimpleClientset(drifted)
	backend.clients.K8s = k8s

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	assert.False(t, changed)
	require.ErrorContains(t, err, "configuration digest changed")
	assert.True(t, nvcaerrors.IsTerminal(err))

	bindings, listErr := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		List(t.Context(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, bindings.Items)
}

// TestEnsureModelCacheBindingToleratesUnrelatedCatalogEdit covers the rollout
// property the catalog exists for: qualifying a new provider edits the catalog
// payload, and that must not terminally fail requests whose own qualified
// profile is unchanged.
func TestEnsureModelCacheBindingToleratesUnrelatedCatalogEdit(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, _ := bindingTestBackend(req)
	assignCreatedBindingUID(bart, types.UID("binding-uid"))
	backend.clients.K8s = fakek8sclient.NewSimpleClientset(
		selectionStorageClass(),
		selectionCatalogConfigMap(selectionCatalogNVMesh+"\n"),
	)

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed)

	bindings, listErr := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		List(t.Context(), metav1.ListOptions{})
	require.NoError(t, listErr)
	require.Len(t, bindings.Items, 1, "the request commits a binding despite the catalog edit")
	decision := bindings.Items[0].Spec.Decision
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, decision.ProfileDigest)
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, decision.CatalogRevision,
		"the edited payload is still recorded for audit")
}

func TestEnsureModelCacheBindingConvergesAcrossRequestNamespaces(t *testing.T) {
	reqA, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	reqB := reqA.DeepCopy()
	reqB.Namespace = "requests-b"
	reqB.Name = "request-b"
	reqB.UID = types.UID("request-b-uid")
	backend, bart, _ := bindingTestBackend(reqA, reqB.DeepCopy())
	assignCreatedBindingUID(bart, types.UID("binding-uid"))

	changed, err := backend.ensureModelCacheBinding(t.Context(), reqA.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = backend.ensureModelCacheBinding(t.Context(), reqB.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed)

	bindings, err := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, bindings.Items, 1)
	binding := &bindings.Items[0]
	assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
		binding, reqA.Namespace, reqA.Name, reqA.UID))
	assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
		binding, reqB.Namespace, reqB.Name, reqB.UID))
	assert.Len(t, binding.Status.RequestReferences, 2)

	for _, req := range []*nvcav2beta1.ICMSRequest{reqA, reqB} {
		persisted, getErr := bart.NvcaV2beta1().ICMSRequests(req.Namespace).
			Get(t.Context(), req.Name, metav1.GetOptions{})
		require.NoError(t, getErr)
		selection, parseErr := nvcastorage.ParsePersistedModelCacheStorageSelection(
			persisted.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey])
		require.NoError(t, parseErr)
		assert.Equal(t, binding.Name, selection.BindingName)
		assert.Equal(t, binding.UID, selection.BindingUID)
	}
}

func TestEnsureModelCacheBindingReplacesOnlyStaleRequestUIDReference(t *testing.T) {
	t.Run("replaced request UID", func(t *testing.T) {
		req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
		binding, err := nvcastorage.NewModelCacheBinding(
			selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
		require.NoError(t, err)
		binding.UID = types.UID("binding-uid")
		binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
		binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
			Namespace: req.Namespace,
			Name:      req.Name,
			UID:       types.UID("old-request-uid"),
		}}
		backend, bart, _ := bindingTestBackend(req, binding)

		changed, ensureErr := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
		require.NoError(t, ensureErr)
		assert.True(t, changed)
		updated, getErr := bart.NvcaV2beta1().ModelCacheBindings(binding.Namespace).
			Get(t.Context(), binding.Name, metav1.GetOptions{})
		require.NoError(t, getErr)
		assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
			updated, req.Namespace, req.Name, req.UID))
		assert.False(t, nvcastorage.ModelCacheBindingHasRequestReference(
			updated, req.Namespace, req.Name, types.UID("old-request-uid")))
	})

	t.Run("old request UID is still live", func(t *testing.T) {
		req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
		oldReq := req.DeepCopy()
		oldReq.UID = types.UID("old-request-uid")
		binding, err := nvcastorage.NewModelCacheBinding(
			selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
		require.NoError(t, err)
		binding.UID = types.UID("binding-uid")
		binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
		binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
			Namespace: oldReq.Namespace,
			Name:      oldReq.Name,
			UID:       oldReq.UID,
		}}
		bart := fakebartclient.NewSimpleClientset(oldReq, binding)
		backend, _, k8s := bindingTestBackend(req)
		backend.clients.BART = bart
		backend.clients.K8s = k8s

		changed, ensureErr := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
		assert.False(t, changed)
		require.ErrorContains(t, ensureErr, "already referenced by live request")
		assert.True(t, nvcaerrors.IsTerminal(ensureErr))
	})
}

func TestEnsureModelCacheBindingReleasesReferenceWhenRequestStartsDeleting(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, _ := bindingTestBackend(req)
	assignCreatedBindingUID(bart, types.UID("binding-uid"))
	deleting := req.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	bart.PrependReactor("get", "icmsrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, deleting.DeepCopy(), nil
	})

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	assert.False(t, changed)
	require.ErrorContains(t, err, "began deleting")
	binding, getErr := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		Get(t.Context(), nvcastorage.ModelCacheBindingName("model-cache-handle"), metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Empty(t, binding.Status.RequestReferences)
}

func TestEnsureModelCacheBindingReleasesReferenceWhenRequestIsReplaced(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, _ := bindingTestBackend(req)
	assignCreatedBindingUID(bart, types.UID("binding-uid"))
	replacement := req.DeepCopy()
	replacement.UID = types.UID("replacement-request-uid")
	bart.PrependReactor("get", "icmsrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, replacement.DeepCopy(), nil
	})

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	assert.False(t, changed)
	require.ErrorContains(t, err, "was replaced")
	binding, getErr := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		Get(t.Context(), nvcastorage.ModelCacheBindingName("model-cache-handle"), metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Empty(t, binding.Status.RequestReferences)
}

func TestEnsureModelCacheBindingRetriesTransientCatalogRead(t *testing.T) {
	req, _ := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	backend, bart, k8s := bindingTestBackend(req)
	k8s.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("catalog API unavailable")
	})

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	assert.False(t, changed)
	require.ErrorContains(t, err, "catalog API unavailable")
	assert.False(t, nvcaerrors.IsTerminal(err))
	bindings, listErr := bart.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		List(t.Context(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, bindings.Items)
}

func TestEnsureModelCacheBindingAdoptsExactEmptyStatus(t *testing.T) {
	req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	binding, err := nvcastorage.NewModelCacheBinding(
		selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	backend, bart, k8s := bindingTestBackend(req, binding)

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Empty(t, k8s.Actions(), "exact binding adoption must not consult the live StorageClass")

	adopted, err := bart.NvcaV2beta1().ModelCacheBindings(binding.Namespace).
		Get(t.Context(), binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, adopted.Status.Phase)
	assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
		adopted, req.Namespace, req.Name, req.UID))
}

func TestEnsureModelCacheBindingStopsStaleReconcileAfterConcurrentCommit(t *testing.T) {
	req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	binding, err := nvcastorage.NewModelCacheBinding(
		selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
		Namespace: req.Namespace,
		Name:      req.Name,
		UID:       req.UID,
	}}
	backend, bart, _ := bindingTestBackend(req, binding)

	latest := req.DeepCopy()
	latest.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] =
		boundSelectionPayload(t, selection, binding)
	_, err = bart.NvcaV2beta1().ICMSRequests(latest.Namespace).
		Update(t.Context(), latest, metav1.UpdateOptions{})
	require.NoError(t, err)

	changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, changed, "a stale unbound reconcile must stop after another reconcile commits the binding")
}

func TestEnsureModelCacheBindingFailsClosedOnRetiringOrCollision(t *testing.T) {
	for _, tt := range []struct {
		name       string
		bindingNCA string
		phase      nvcav2beta1.ModelCacheBindingPhase
		want       string
	}{
		{name: "retiring", bindingNCA: "nca-a", phase: nvcav2beta1.ModelCacheBindingPhaseRetiring, want: "Retiring"},
		{name: "handle collision", bindingNCA: "nca-b", phase: nvcav2beta1.ModelCacheBindingPhaseActive, want: "different sharing domain"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
			binding, err := nvcastorage.NewModelCacheBinding(
				selection, tt.bindingNCA, "model-cache-handle", "pod-instances")
			require.NoError(t, err)
			binding.UID = types.UID("binding-uid")
			binding.Status.Phase = tt.phase
			backend, _, _ := bindingTestBackend(req, binding)

			changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
			assert.False(t, changed)
			require.ErrorContains(t, err, tt.want)
			assert.True(t, nvcaerrors.IsTerminal(err))
		})
	}
}

func TestValidateModelCacheBindingForRuntimeRequiresExactRequestReference(t *testing.T) {
	req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	binding, err := nvcastorage.NewModelCacheBinding(
		selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] =
		boundSelectionPayload(t, selection, binding)
	backend, _, _ := bindingTestBackend(req, binding)

	err = backend.validateModelCacheBindingForRuntime(t.Context(), req.DeepCopy())
	require.ErrorContains(t, err, "has no reference")
	assert.True(t, nvcaerrors.IsTerminal(err))

	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{{
		Namespace: req.Namespace,
		Name:      req.Name,
		UID:       req.UID,
	}}
	backend, _, _ = bindingTestBackend(req, binding)
	require.NoError(t, backend.validateModelCacheBindingForRuntime(t.Context(), req.DeepCopy()))
}

func TestBeginRegularModelCacheBindingRetirementTransitionsSoleReference(t *testing.T) {
	req, binding, backend, bart := boundModelCacheBindingFixture(
		t,
		nvcastorage.ModelCacheWorkflowRegular,
		nvcav2beta1.ModelCacheBindingPhaseActive,
		[]nvcav2beta1.ModelCacheBindingRequestReference{{
			Namespace: "requests",
			Name:      "request",
			UID:       types.UID("request-uid"),
		}},
	)

	retiring, authorized, err := backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, authorized)
	require.NotNil(t, retiring)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, retiring.Status.Phase)
	assert.NotNil(t, retiring.Status.LastPhaseTransitionTime)
	assert.Equal(t, binding.Status.RequestReferences, retiring.Status.RequestReferences)
	assert.Equal(t, 1, countModelCacheBindingStatusUpdates(bart.Actions()))

	firstTransitionTime := retiring.Status.LastPhaseTransitionTime.DeepCopy()
	bart.ClearActions()
	retiring, authorized, err = backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, authorized)
	require.NotNil(t, retiring)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, retiring.Status.Phase)
	assert.Equal(t, firstTransitionTime, retiring.Status.LastPhaseTransitionTime)
	assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()),
		"an interrupted cleanup must adopt Retiring without returning it to Active")

	_, runtimeErr := backend.activeModelCacheBindingForRuntime(t.Context(), req.DeepCopy())
	require.ErrorIs(t, runtimeErr, errRegularModelCacheBindingRetiring)
	assert.True(t, nvcaerrors.IsTerminal(runtimeErr))
}
func TestBeginRegularModelCacheBindingRetirementResumesAfterReferenceRelease(t *testing.T) {
	req, binding, backend, bart := boundModelCacheBindingFixture(
		t,
		nvcastorage.ModelCacheWorkflowRegular,
		nvcav2beta1.ModelCacheBindingPhaseRetiring,
		nil,
	)
	bart.ClearActions()

	got, authorized, err := backend.beginRegularModelCacheBindingRetirement(
		t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.True(t, authorized)
	require.NotNil(t, got)
	assert.Equal(t, binding.UID, got.UID)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, got.Status.Phase)
	assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()))
}

func TestBeginRegularModelCacheBindingRetirementOtherReferenceBlocksWithoutMutation(t *testing.T) {
	req, _, backend, bart := boundModelCacheBindingFixture(
		t,
		nvcastorage.ModelCacheWorkflowRegular,
		nvcav2beta1.ModelCacheBindingPhaseActive,
		[]nvcav2beta1.ModelCacheBindingRequestReference{
			{Namespace: "requests", Name: "request", UID: types.UID("request-uid")},
			{Namespace: "requests", Name: "other", UID: types.UID("other-uid")},
		},
	)

	got, authorized, err := backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.False(t, authorized)
	require.NotNil(t, got)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, got.Status.Phase)
	assert.Nil(t, got.Status.LastPhaseTransitionTime)
	assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()))
}

func TestBeginRegularModelCacheBindingRetirementConcurrentReferenceWinsConflict(t *testing.T) {
	req, binding, backend, bart := boundModelCacheBindingFixture(
		t,
		nvcastorage.ModelCacheWorkflowRegular,
		nvcav2beta1.ModelCacheBindingPhaseActive,
		[]nvcav2beta1.ModelCacheBindingRequestReference{{
			Namespace: "requests",
			Name:      "request",
			UID:       types.UID("request-uid"),
		}},
	)
	gvr := nvcav2beta1.SchemeGroupVersion.WithResource("modelcachebindings")
	updateAttempts := 0
	bart.PrependReactor("update", "modelcachebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		updateAttempts++
		if updateAttempts != 1 {
			return false, nil, nil
		}
		tracked, err := bart.Tracker().Get(gvr, binding.Namespace, binding.Name)
		if err != nil {
			return true, nil, err
		}
		concurrent := tracked.(*nvcav2beta1.ModelCacheBinding).DeepCopy()
		concurrent.ResourceVersion = "2"
		concurrent.Status.RequestReferences = append(
			concurrent.Status.RequestReferences,
			nvcav2beta1.ModelCacheBindingRequestReference{
				Namespace: "requests",
				Name:      "concurrent",
				UID:       types.UID("concurrent-uid"),
			},
		)
		if err := bart.Tracker().Update(gvr, concurrent, binding.Namespace); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(
			nvcav2beta1.Resource("modelcachebindings"), binding.Name, errors.New("stale status resource version"))
	})

	got, authorized, err := backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
	require.NoError(t, err)
	assert.False(t, authorized)
	require.NotNil(t, got)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, got.Status.Phase)
	assert.Len(t, got.Status.RequestReferences, 2)
	assert.Equal(t, 1, updateAttempts, "the conflict retry must observe the new reference and stop writing")

	stored, err := bart.NvcaV2beta1().ModelCacheBindings(binding.Namespace).
		Get(t.Context(), binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseActive, stored.Status.Phase)
	assert.Len(t, stored.Status.RequestReferences, 2)
}

func TestBeginRegularModelCacheBindingRetirementRejectsForeignOrMissingReference(t *testing.T) {
	for _, tt := range []struct {
		name string
		refs []nvcav2beta1.ModelCacheBindingRequestReference
	}{
		{name: "missing"},
		{
			name: "foreign UID",
			refs: []nvcav2beta1.ModelCacheBindingRequestReference{{
				Namespace: "requests",
				Name:      "request",
				UID:       types.UID("foreign-uid"),
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, _, backend, bart := boundModelCacheBindingFixture(
				t, nvcastorage.ModelCacheWorkflowRegular,
				nvcav2beta1.ModelCacheBindingPhaseActive, tt.refs)

			got, authorized, err := backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
			assert.Nil(t, got)
			assert.False(t, authorized)
			require.ErrorContains(t, err, "has no reference")
			assert.True(t, nvcaerrors.IsTerminal(err))
			assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()))
		})
	}
}

func TestEnsureModelCacheBindingAllowsOnlySoleRegularRequestToResumeRetiringCleanup(t *testing.T) {
	for _, tt := range []struct {
		name      string
		workflow  nvcastorage.ModelCacheWorkflow
		refs      []nvcav2beta1.ModelCacheBindingRequestReference
		wantError string
	}{
		{
			name:     "sole regular request",
			workflow: nvcastorage.ModelCacheWorkflowRegular,
			refs: []nvcav2beta1.ModelCacheBindingRequestReference{{
				Namespace: "requests",
				Name:      "request",
				UID:       types.UID("request-uid"),
			}},
		},
		{
			name:     "Helm request",
			workflow: nvcastorage.ModelCacheWorkflowHelm,
			refs: []nvcav2beta1.ModelCacheBindingRequestReference{{
				Namespace: "requests",
				Name:      "request",
				UID:       types.UID("request-uid"),
			}},
			wantError: "cannot serve",
		},
		{
			name:      "missing regular reference",
			workflow:  nvcastorage.ModelCacheWorkflowRegular,
			wantError: "has no reference",
		},
		{
			name:     "regular request with another reference",
			workflow: nvcastorage.ModelCacheWorkflowRegular,
			refs: []nvcav2beta1.ModelCacheBindingRequestReference{
				{Namespace: "requests", Name: "request", UID: types.UID("request-uid")},
				{Namespace: "requests", Name: "other", UID: types.UID("other-uid")},
			},
			wantError: "other request references",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, _, backend, bart := boundModelCacheBindingFixture(
				t, tt.workflow, nvcav2beta1.ModelCacheBindingPhaseRetiring, tt.refs)
			bart.ClearActions()

			changed, err := backend.ensureModelCacheBinding(t.Context(), req.DeepCopy())
			assert.False(t, changed)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
				assert.True(t, nvcaerrors.IsTerminal(err))
			}
			assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()),
				"ensure must never transition Retiring back to Active")
		})
	}
}

func TestBeginRegularModelCacheBindingRetirementRejectsHelm(t *testing.T) {
	req, _, backend, bart := boundModelCacheBindingFixture(
		t,
		nvcastorage.ModelCacheWorkflowHelm,
		nvcav2beta1.ModelCacheBindingPhaseActive,
		[]nvcav2beta1.ModelCacheBindingRequestReference{{
			Namespace: "requests",
			Name:      "request",
			UID:       types.UID("request-uid"),
		}},
	)

	got, authorized, err := backend.beginRegularModelCacheBindingRetirement(t.Context(), req.DeepCopy())
	assert.Nil(t, got)
	assert.False(t, authorized)
	require.ErrorContains(t, err, "durable regular binding")
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.Zero(t, countModelCacheBindingStatusUpdates(bart.Actions()))
}

func TestReleaseModelCacheBindingReferenceIsExactAndIdempotent(t *testing.T) {
	req, selection := durableModelCacheBindingRequest(t, nvcastorage.ModelCacheWorkflowRegular)
	binding, err := nvcastorage.NewModelCacheBinding(
		selection, req.Spec.NCAId, "model-cache-handle", "pod-instances")
	require.NoError(t, err)
	binding.UID = types.UID("binding-uid")
	binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	binding.Status.RequestReferences = []nvcav2beta1.ModelCacheBindingRequestReference{
		{Namespace: req.Namespace, Name: req.Name, UID: req.UID},
		{Namespace: req.Namespace, Name: "other", UID: types.UID("other-uid")},
	}
	req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] =
		boundSelectionPayload(t, selection, binding)
	backend, bart, _ := bindingTestBackend(req, binding)

	require.NoError(t, backend.releaseModelCacheBindingReference(t.Context(), req.DeepCopy()))
	require.NoError(t, backend.releaseModelCacheBindingReference(t.Context(), req.DeepCopy()))
	updated, err := bart.NvcaV2beta1().ModelCacheBindings(binding.Namespace).
		Get(t.Context(), binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.False(t, nvcastorage.ModelCacheBindingHasRequestReference(
		updated, req.Namespace, req.Name, req.UID))
	assert.True(t, nvcastorage.ModelCacheBindingHasRequestReference(
		updated, req.Namespace, "other", types.UID("other-uid")))
}

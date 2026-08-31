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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type rwxReadOnlyRuntimeFixture struct {
	backend K8sComputeBackend
	req     *nvcav2beta1.ICMSRequest
	binding *nvcav2beta1.ModelCacheBinding
	rwPVC   *corev1.PersistentVolumeClaim
	job     *batchv1.Job
}

func addRWXReadOnlyRequestOwnership(obj metav1.Object, requestID string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[nvcatypes.ICMSRequestIDKey] = requestID
	labels[nvcatypes.MessageBatchIDKey] = "batch-" + requestID
	labels[nvcatypes.FunctionVersionIDKey] = "version-" + requestID
	obj.SetLabels(labels)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[nvcatypes.ICMSRequestIDKey] = requestID
	annotations[nvcatypes.InstanceCountKey] = "1"
	obj.SetAnnotations(annotations)
	obj.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: nvcav2beta1.SchemeGroupVersion.String(),
		Kind:       "ICMSRequest",
		Name:       requestID,
		UID:        types.UID("uid-" + requestID),
	}})
}

func assertRWXReadOnlyBindingScopedMetadata(t *testing.T, obj metav1.Object) {
	t.Helper()
	assert.Empty(t, obj.GetOwnerReferences())
	assert.NotContains(t, obj.GetLabels(), nvcatypes.ICMSRequestIDKey)
	assert.NotContains(t, obj.GetLabels(), nvcatypes.MessageBatchIDKey)
	assert.NotContains(t, obj.GetLabels(), nvcatypes.FunctionVersionIDKey)
	assert.NotContains(t, obj.GetAnnotations(), nvcatypes.ICMSRequestIDKey)
	assert.NotContains(t, obj.GetAnnotations(), nvcatypes.InstanceCountKey)
}

func newRWXReadOnlyRuntimeFixture(
	t *testing.T,
	provider string,
	provisioner string,
) *rwxReadOnlyRuntimeFixture {
	t.Helper()

	resolved := &nvcastorage.ModelCacheStorageSelection{
		StorageClassName:    nvcastorage.DefaultModelCacheStorageClassName,
		StorageClassUID:     types.UID("storage-class-uid"),
		StorageClassDigest:  "v1:sha256:storage-class-digest",
		ProfileDigest:       "sha256:catalog-digest",
		Provider:            provider,
		Provisioner:         provisioner,
		Transition:          nvcastorage.ModelCacheTransitionRWXReadOnly,
		RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
	}
	return newRWXReadOnlyRuntimeFixtureFromSelection(
		t, resolved, fakek8sclient.NewSimpleClientset())
}

func newResolvedWekaRWXReadOnlyRuntimeFixture(t *testing.T) *rwxReadOnlyRuntimeFixture {
	t.Helper()
	storageClass := selectionStorageClassForProvisioner("csi.weka.io")
	catalog := selectionCatalogConfigMap(selectionCatalogRWXReadOnly)
	k8sClient := fakek8sclient.NewSimpleClientset(storageClass, catalog)
	resolved, err := nvcastorage.ResolveModelCacheStorageWithClientset(
		t.Context(), k8sClient, selectionCatalogNamespace,
		nvcastorage.ModelCacheWorkflowRegular)
	require.NoError(t, err)
	require.Equal(t, nvcastorage.ModelCacheTransitionRWXReadOnly, resolved.Transition)
	return newRWXReadOnlyRuntimeFixtureFromSelection(t, resolved, k8sClient)
}

func newRWXReadOnlyRuntimeFixtureFromSelection(
	t *testing.T,
	resolved *nvcastorage.ModelCacheStorageSelection,
	k8sClient *fakek8sclient.Clientset,
) *rwxReadOnlyRuntimeFixture {
	t.Helper()
	raw := persistedSelectionAnnotation(
		t,
		nvcastorage.ModelCacheWorkflowRegular,
		nvcastorage.ModelCacheSelectionDurable,
		resolved,
	)

	backend := testContainerModelCacheBackend(k8sClient)
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "request", Namespace: RequestsNamespace},
	}
	binding := installActiveRegularModelCacheBinding(t, &backend, req, raw)
	require.Equal(t, nvcastorage.ModelCacheTransitionRWXReadOnly, binding.Spec.Decision.Transition)
	require.Equal(t, []string{"rw-pvc-model-cache-handle"},
		binding.Spec.Resources.PersistentVolumeClaimNames)

	bindingLabels := map[string]string{
		nvcastorage.ModelCacheBindingUIDLabelKey: string(binding.UID),
	}
	automountServiceAccountToken := false
	storageClassName := nvcastorage.DefaultModelCacheStorageClassName
	rwPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      binding.Spec.Resources.PersistentVolumeClaimNames[0],
			Namespace: binding.Spec.Resources.WriterNamespace,
			Labels:    bindingLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: &storageClassName,
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      binding.Spec.Resources.JobNames[0],
			Namespace: binding.Spec.Resources.WriterNamespace,
			Labels:    bindingLabels,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: bindingLabels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automountServiceAccountToken,
					Volumes: []corev1.Volume{{
						Name: ModelVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: rwPVC.Name,
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "writer",
						Image: "example.invalid/model-cache-writer:test",
						VolumeMounts: []corev1.VolumeMount{{
							Name: ModelVolumeName, MountPath: "/models"}},
					}},
				},
			},
		},
	}

	return &rwxReadOnlyRuntimeFixture{
		backend: backend,
		req:     req,
		binding: binding,
		rwPVC:   rwPVC,
		job:     job,
	}
}

func TestRWXReadOnlyCreatesWriterFromResolvedProvider(t *testing.T) {
	fixture := newResolvedWekaRWXReadOnlyRuntimeFixture(t)
	k8sClient := fixture.backend.clients.K8s.(*fakek8sclient.Clientset)
	for _, obj := range []metav1.Object{
		fixture.rwPVC, fixture.job, &fixture.job.Spec.Template.ObjectMeta,
	} {
		addRWXReadOnlyRequestOwnership(obj, "request-a")
	}
	require.NoError(t, fixture.backend.prepareRegularModelCacheBindingResources(
		t.Context(), fixture.binding, fixture.rwPVC, fixture.job))
	for _, obj := range []metav1.Object{
		fixture.rwPVC, fixture.job, &fixture.job.Spec.Template.ObjectMeta,
	} {
		assertRWXReadOnlyBindingScopedMetadata(t, obj)
	}
	installRWXReadOnlyCreateIdentityReactors(t, k8sClient)
	k8sClient.ClearActions()

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingInProgress, state)
	assert.Empty(t, claimName, "NVCA must not publish the claim before the writer completes")

	createdPVC, err := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		createdPVC.Spec.AccessModes)
	require.NotNil(t, createdPVC.Spec.StorageClassName)
	assert.Equal(t, nvcastorage.DefaultModelCacheStorageClassName,
		*createdPVC.Spec.StorageClassName)
	assert.Equal(t, string(fixture.binding.UID),
		createdPVC.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
	assertRWXReadOnlyBindingScopedMetadata(t, createdPVC)
	assert.NotContains(t, createdPVC.Labels, nvcastorage.ModelCachePopulatedLabelKey)

	createdJob, err := k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, string(fixture.binding.UID),
		createdJob.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
	assert.Equal(t, string(fixture.binding.UID),
		createdJob.Spec.Template.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
	assertRWXReadOnlyBindingScopedMetadata(t, createdJob)
	assertRWXReadOnlyBindingScopedMetadata(t, &createdJob.Spec.Template.ObjectMeta)
	assert.Equal(t, string(createdPVC.UID),
		createdJob.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey])

	actions := k8sClient.Actions()
	assert.Equal(t, 1, countRWXReadOnlyAction(actions, "create", "persistentvolumeclaims"))
	assert.Equal(t, 1, countRWXReadOnlyAction(actions, "create", "jobs"))
	for _, action := range actions {
		resource := action.GetResource().Resource
		name := rwxReadOnlyActionName(action)
		switch resource {
		case "persistentvolumes", "volumeattachments":
			t.Errorf("rwxReadOnly must not access %s: %s %s",
				resource, action.GetVerb(), name)
		case "persistentvolumeclaims":
			assert.Equal(t, fixture.rwPVC.Name, name,
				"writer setup must not address a second PVC")
		case "jobs":
			assert.Equal(t, fixture.job.Name, name,
				"writer setup must only address its recorded Job")
		}
	}
}

func TestPrepareRWXReadOnlyResourcesSupportsSharedRequestReuse(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	firstPVC := fixture.rwPVC.DeepCopy()
	firstJob := fixture.job.DeepCopy()
	for _, obj := range []metav1.Object{
		firstPVC, firstJob, &firstJob.Spec.Template.ObjectMeta,
	} {
		addRWXReadOnlyRequestOwnership(obj, "request-a")
	}
	require.NoError(t, fixture.backend.prepareRegularModelCacheBindingResources(
		t.Context(), fixture.binding, firstPVC, firstJob))
	for _, obj := range []metav1.Object{
		firstPVC, firstJob, &firstJob.Spec.Template.ObjectMeta,
	} {
		assertRWXReadOnlyBindingScopedMetadata(t, obj)
	}

	firstPVC.UID = types.UID("shared-pvc-uid")
	firstPVC.ResourceVersion = "shared-pvc-rv"
	firstPVC.Spec.VolumeName = "shared-pv"
	firstPVC.Status.Phase = corev1.ClaimBound
	firstPVC.Labels[nvcastorage.ModelCachePopulatedLabelKey] =
		nvcastorage.ModelCachePopulatedLabelValue
	firstJob.UID = types.UID("shared-job-uid")
	firstJob.ResourceVersion = "shared-job-rv"
	now := metav1.Now()
	firstJob.Status.Succeeded = 1
	firstJob.Status.CompletionTime = &now
	require.NoError(t, bindRegularModelCacheWriterJobToPVC(
		firstJob, firstPVC, fixture.binding))
	sharedPV := fixture.boundPV(firstPVC)
	k8sClient := fixture.useK8sObjects(firstPVC.DeepCopy(), sharedPV, firstJob.DeepCopy())
	secondPVC := fixture.rwPVC.DeepCopy()
	secondJob := fixture.job.DeepCopy()
	for _, obj := range []metav1.Object{
		secondPVC, secondJob, &secondJob.Spec.Template.ObjectMeta,
	} {
		addRWXReadOnlyRequestOwnership(obj, "request-b")
	}
	k8sClient.ClearActions()
	require.NoError(t, fixture.backend.prepareRegularModelCacheBindingResources(
		t.Context(), fixture.binding, secondPVC, secondJob))
	for _, obj := range []metav1.Object{
		secondPVC, secondJob, &secondJob.Spec.Template.ObjectMeta,
	} {
		assertRWXReadOnlyBindingScopedMetadata(t, obj)
	}
	storedBinding, err := fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	secondReq := fixture.req.DeepCopy()
	secondReq.Name = "request-b"
	secondReq.UID = types.UID("request-b-uid")
	storedBinding.Status.RequestReferences = append(
		storedBinding.Status.RequestReferences,
		nvcav2beta1.ModelCacheBindingRequestReference{
			Namespace: secondReq.Namespace,
			Name:      secondReq.Name,
			UID:       secondReq.UID,
		})
	_, err = fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(storedBinding.Namespace).
		UpdateStatus(t.Context(), storedBinding, metav1.UpdateOptions{})
	require.NoError(t, err)
	_, err = fixture.backend.clients.BART.NvcaV2beta1().
		ICMSRequests(secondReq.Namespace).Create(t.Context(), secondReq, metav1.CreateOptions{})
	require.NoError(t, err)

	mutate, claimName, runtimeErr := fixture.backend.setupContainerModelCaching(
		newTestContext(), secondReq, secondPVC.DeepCopy(), secondJob.DeepCopy(), nil)
	require.NoError(t, runtimeErr)
	require.NotNil(t, mutate)
	assert.Equal(t, firstPVC.Name, claimName)
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestPrepareRWXReadOnlyResourcesRejectsRequestOwnedExistingObject(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	foreignPVC := fixture.rwPVC.DeepCopy()
	addRWXReadOnlyRequestOwnership(foreignPVC, "request-a")
	fixture.useK8sObjects(foreignPVC)

	err := fixture.backend.prepareRegularModelCacheBindingResources(
		t.Context(), fixture.binding, fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy())
	require.ErrorContains(t, err, "request owner references")
	assert.True(t, nvcaerrors.IsTerminal(err))
}

func TestPrepareRWXReadOnlyResourcesRejectsRequestScopedJobPodTemplate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*metav1.ObjectMeta)
		wantErr string
	}{
		{
			name: "owner reference",
			mutate: func(meta *metav1.ObjectMeta) {
				meta.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: nvcav2beta1.SchemeGroupVersion.String(),
					Kind:       "ICMSRequest",
					Name:       "request-a",
					UID:        types.UID("request-a-uid"),
				}}
			},
			wantErr: "request owner references",
		},
		{
			name: "request label",
			mutate: func(meta *metav1.ObjectMeta) {
				meta.Labels[nvcatypes.ICMSRequestIDKey] = "request-a"
			},
			wantErr: "request-scoped label",
		},
		{
			name: "request annotation",
			mutate: func(meta *metav1.ObjectMeta) {
				meta.Annotations = map[string]string{
					nvcatypes.InstanceCountKey: "1",
				}
			},
			wantErr: "request-scoped annotation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			existingJob := fixture.job.DeepCopy()
			tt.mutate(&existingJob.Spec.Template.ObjectMeta)
			k8sClient := fixture.useK8sObjects(existingJob)

			err := fixture.backend.prepareRegularModelCacheBindingResources(
				t.Context(), fixture.binding, fixture.rwPVC.DeepCopy(), fixture.job.DeepCopy())
			require.ErrorContains(t, err, "writer Job Pod template")
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assertNoKubernetesWrites(t, k8sClient.Actions())
		})
	}
}

func TestRWXReadOnlyRejectsMissingPVCWithExistingJob(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	job := fixture.writerJob()
	k8sClient := fixture.useK8sObjects(job)

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorContains(t, err, "is missing while writer Job")
	assert.True(t, nvcaerrors.IsTerminal(err))
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestRWXReadOnlyAdoptsExactCreateRaceWinners(t *testing.T) {
	fixture := newResolvedWekaRWXReadOnlyRuntimeFixture(t)
	k8sClient := fixture.backend.clients.K8s.(*fakek8sclient.Clientset)
	pvcRaceWon, jobRaceWon := false, false
	k8sClient.Fake.PrependReactor(
		"create", "persistentvolumeclaims",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
			created.UID = types.UID("race-winner-pvc-uid")
			created.ResourceVersion = "race-winner-pvc-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"),
				created, created.Namespace))
			pvcRaceWon = true
			return true, nil, apierrors.NewAlreadyExists(
				corev1.Resource("persistentvolumeclaims"), created.Name)
		})
	k8sClient.Fake.PrependReactor(
		"create", "jobs",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
			created.UID = types.UID("race-winner-job-uid")
			created.ResourceVersion = "race-winner-job-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				batchv1.SchemeGroupVersion.WithResource("jobs"),
				created, created.Namespace))
			jobRaceWon = true
			return true, nil, apierrors.NewAlreadyExists(
				corev1.Resource("jobs"), created.Name)
		})
	k8sClient.ClearActions()

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingInProgress, state)
	assert.Empty(t, claimName)
	assert.True(t, pvcRaceWon)
	assert.True(t, jobRaceWon)
	storedPVC, err := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, string(fixture.binding.UID),
		storedPVC.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
	storedJob, err := k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, string(fixture.binding.UID),
		storedJob.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
}

func (f *rwxReadOnlyRuntimeFixture) boundPVC(populated bool) *corev1.PersistentVolumeClaim {
	pvc := f.rwPVC.DeepCopy()
	pvc.UID = types.UID("writer-pvc-uid")
	pvc.ResourceVersion = "writer-pvc-rv"
	pvc.Spec.VolumeName = "pv-model-cache-handle"
	pvc.Status.Phase = corev1.ClaimBound
	if populated {
		pvc.Labels[nvcastorage.ModelCachePopulatedLabelKey] =
			nvcastorage.ModelCachePopulatedLabelValue
	}
	return pvc
}

func (f *rwxReadOnlyRuntimeFixture) boundPV(
	pvc *corev1.PersistentVolumeClaim,
) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvc.Spec.VolumeName},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName:              nvcastorage.DefaultModelCacheStorageClassName,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Namespace: pvc.Namespace,
				Name:      pvc.Name,
				UID:       pvc.UID,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       f.binding.Spec.Decision.Provisioner,
					VolumeHandle: "model-cache-volume-handle",
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
}

func (f *rwxReadOnlyRuntimeFixture) writerJob() *batchv1.Job {
	job := f.job.DeepCopy()
	job.UID = types.UID("writer-job-uid")
	job.ResourceVersion = "writer-job-rv"
	if job.Spec.Template.Annotations == nil {
		job.Spec.Template.Annotations = map[string]string{}
	}
	job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey] = "writer-pvc-uid"
	return job
}

func (f *rwxReadOnlyRuntimeFixture) completedWriterJob() *batchv1.Job {
	job := f.writerJob()
	now := metav1.Now()
	job.Status.Succeeded = 1
	job.Status.CompletionTime = &now
	return job
}

func (f *rwxReadOnlyRuntimeFixture) useK8sObjects(
	objects ...runtime.Object,
) *fakek8sclient.Clientset {
	k8sClient := fakek8sclient.NewSimpleClientset(objects...)
	f.backend.clients.K8s = k8sClient
	f.backend.bk8s.clients.K8s = k8sClient
	return k8sClient
}

func installRWXReadOnlyCreateIdentityReactors(
	t *testing.T,
	k8sClient *fakek8sclient.Clientset,
) {
	t.Helper()
	k8sClient.Fake.PrependReactor(
		"create", "persistentvolumeclaims",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).
				GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
			created.UID = types.UID("api-created-writer-pvc-uid")
			created.ResourceVersion = "api-created-writer-pvc-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"),
				created, created.Namespace))
			return true, created, nil
		})
	k8sClient.Fake.PrependReactor(
		"create", "jobs",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).
				GetObject().(*batchv1.Job).DeepCopy()
			created.UID = types.UID("api-created-writer-job-uid")
			created.ResourceVersion = "api-created-writer-job-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				batchv1.SchemeGroupVersion.WithResource("jobs"),
				created, created.Namespace))
			return true, created, nil
		})
}

func runRWXReadOnlySetup(
	t *testing.T,
	fixture *rwxReadOnlyRuntimeFixture,
) (ModelCachingState, string, error) {
	t.Helper()
	return fixture.backend.SetupModelCachingForRequest(
		newTestContext(),
		fixture.rwPVC.DeepCopy(),
		fixture.job.DeepCopy(),
		fixture.req,
		false,
		nil,
	)
}

func rwxReadOnlyActionName(action k8stesting.Action) string {
	if named, ok := action.(interface{ GetName() string }); ok {
		return named.GetName()
	}
	if objectAction, ok := action.(interface{ GetObject() runtime.Object }); ok {
		if object, ok := objectAction.GetObject().(metav1.Object); ok {
			return object.GetName()
		}
	}
	return ""
}

func assertRWXReadOnlyActionTrace(
	t *testing.T,
	actions []k8stesting.Action,
	wantPVCName string,
	wantJobName string,
) {
	t.Helper()
	for _, action := range actions {
		resource := action.GetResource().Resource
		verb := action.GetVerb()
		name := rwxReadOnlyActionName(action)
		switch resource {
		case "persistentvolumes":
			assert.Equal(t, "get", verb,
				"rwxReadOnly may validate a PV but must not modify it")
		case "volumeattachments":
			t.Errorf("rwxReadOnly must not access %s: %s %s", resource, verb, name)
		case "persistentvolumeclaims":
			assert.Equal(t, wantPVCName, name,
				"rwxReadOnly must not address a second PVC")
			assert.NotEqual(t, "create", verb,
				"an existing populated/unpopulated writer claim must not be replaced")
			assert.NotEqual(t, "delete", verb,
				"rwxReadOnly publication must retain the writer claim")
			assert.NotEqual(t, "delete-collection", verb,
				"rwxReadOnly publication must retain the writer claim")
		case "jobs":
			assert.Equal(t, wantJobName, name,
				"rwxReadOnly must only address its recorded writer Job")
		}
	}
}

func countRWXReadOnlyAction(
	actions []k8stesting.Action,
	verb string,
	resource string,
) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestRWXReadOnlyCompletedWriterPublishesSameClaim(t *testing.T) {
	providers := []struct {
		name        string
		provider    string
		provisioner string
	}{
		{name: "Weka", provider: "weka", provisioner: "csi.weka.io"},
		{name: "OCI FSS", provider: "ociFss", provisioner: "fss.csi.oraclecloud.com"},
	}
	for _, tt := range providers {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, tt.provider, tt.provisioner)
			pvc := fixture.boundPVC(false)
			pv := fixture.boundPV(pvc)
			job := fixture.completedWriterJob()
			k8sClient := fixture.useK8sObjects(pvc, pv, job)

			state, claimName, err := runRWXReadOnlySetup(t, fixture)
			require.NoError(t, err)
			assert.Equal(t, ModelCachingCompleted, state)
			assert.Equal(t, fixture.rwPVC.Name, claimName)

			actions := k8sClient.Actions()
			assertRWXReadOnlyActionTrace(t, actions, fixture.rwPVC.Name, fixture.job.Name)
			assert.Equal(t, 1, countRWXReadOnlyAction(actions, "update", "persistentvolumeclaims"))
			assert.Equal(t, 0, countRWXReadOnlyAction(actions, "delete", "jobs"))
			assert.Equal(t, 0, countRWXReadOnlyAction(actions, "create", "jobs"))

			storedPVC, getErr := k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).
				Get(t.Context(), pvc.Name, metav1.GetOptions{})
			require.NoError(t, getErr)
			assert.Equal(t, nvcastorage.ModelCachePopulatedLabelValue,
				storedPVC.Labels[nvcastorage.ModelCachePopulatedLabelKey])
			storedJob, getErr := k8sClient.BatchV1().Jobs(job.Namespace).
				Get(t.Context(), job.Name, metav1.GetOptions{})
			require.NoError(t, getErr)
			assert.Equal(t, job.UID, storedJob.UID,
				"the completed Job must remain as the publication fence")

			k8sClient.ClearActions()
			state, claimName, err = runRWXReadOnlySetup(t, fixture)
			require.NoError(t, err)
			assert.Equal(t, ModelCachingCompleted, state)
			assert.Equal(t, fixture.rwPVC.Name, claimName)
			assert.Equal(t, 0, countRWXReadOnlyAction(
				k8sClient.Actions(), "create", "jobs"))
			assert.Equal(t, 0, countRWXReadOnlyAction(
				k8sClient.Actions(), "delete", "jobs"))
		})
	}
}

func TestRWXReadOnlyRejectsMissingPublicationFence(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(true)
	pv := fixture.boundPV(pvc)
	k8sClient := fixture.useK8sObjects(pvc, pv)

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorContains(t, err, "publication fence Job")
	assert.True(t, nvcaerrors.IsTerminal(err))

	actions := k8sClient.Actions()
	assertRWXReadOnlyActionTrace(t, actions, pvc.Name, fixture.job.Name)
	assert.Equal(t, 0, countRWXReadOnlyAction(actions, "create", "jobs"))
	assert.Equal(t, 0, countRWXReadOnlyAction(actions, "delete", "jobs"))
	assert.Equal(t, 0, countRWXReadOnlyAction(actions, "update", "persistentvolumeclaims"))
}

func TestRWXReadOnlyUnpopulatedClaimRestartsWriter(t *testing.T) {
	fixture := newResolvedWekaRWXReadOnlyRuntimeFixture(t)
	pvc := fixture.boundPVC(false)
	pv := fixture.boundPV(pvc)
	k8sClient := fixture.useK8sObjects(
		pvc, pv, selectionStorageClassForProvisioner("csi.weka.io"))

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingInProgress, state)
	assert.Empty(t, claimName, "an unpopulated claim must never be published")

	actions := k8sClient.Actions()
	assertRWXReadOnlyActionTrace(t, actions, pvc.Name, fixture.job.Name)
	assert.Equal(t, 1, countRWXReadOnlyAction(actions, "create", "jobs"))
	assert.Equal(t, 0, countRWXReadOnlyAction(actions, "update", "persistentvolumeclaims"))
	createdJob, getErr := k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, string(fixture.binding.UID),
		createdJob.Labels[nvcastorage.ModelCacheBindingUIDLabelKey])
}

func TestRWXReadOnlyRejectsForeignOwnership(t *testing.T) {
	t.Run("writer PVC", func(t *testing.T) {
		fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
		pvc := fixture.boundPVC(false)
		pvc.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] = "foreign-binding"
		k8sClient := fixture.useK8sObjects(pvc)

		state, claimName, err := runRWXReadOnlySetup(t, fixture)
		assert.Equal(t, ModelCachingFailed, state)
		assert.Empty(t, claimName)
		require.ErrorContains(t, err, "foreign-binding")
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertRWXReadOnlyActionTrace(t, k8sClient.Actions(), pvc.Name, fixture.job.Name)
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})

	t.Run("writer Job", func(t *testing.T) {
		fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
		pvc := fixture.boundPVC(false)
		pv := fixture.boundPV(pvc)
		job := fixture.writerJob()
		job.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] = "foreign-binding"
		k8sClient := fixture.useK8sObjects(pvc, pv, job)

		state, claimName, err := runRWXReadOnlySetup(t, fixture)
		assert.Equal(t, ModelCachingFailed, state)
		assert.Empty(t, claimName)
		require.ErrorContains(t, err, "foreign-binding")
		assert.True(t, nvcaerrors.IsTerminal(err))
		assertRWXReadOnlyActionTrace(t, k8sClient.Actions(), pvc.Name, fixture.job.Name)
		assertNoKubernetesWrites(t, k8sClient.Actions())
	})
}

func TestRWXReadOnlyRejectsActiveWriterBehindPopulatedMarker(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(true)
	pv := fixture.boundPV(pvc)
	job := fixture.writerJob()
	job.Status.Active = 1
	k8sClient := fixture.useK8sObjects(pvc, pv, job)

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorContains(t, err, "populated rwxReadOnly writer PVC has non-completed Job")
	assert.True(t, nvcaerrors.IsTerminal(err))
	assertRWXReadOnlyActionTrace(t, k8sClient.Actions(), pvc.Name, fixture.job.Name)
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestRWXReadOnlyRejectsDriftedCompletedPublicationFence(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*batchv1.Job)
		wantErr string
	}{
		{
			name: "foreign binding UID",
			mutate: func(job *batchv1.Job) {
				job.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] = "foreign-binding"
			},
			wantErr: "foreign-binding",
		},
		{
			name: "immutable writer spec",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Containers[0].Image = "example.invalid/foreign:latest"
			},
			wantErr: "immutable spec does not match intent",
		},
		{
			name: "recreated PVC UID witness",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey] =
					"previous-writer-pvc-uid"
			},
			wantErr: "records PVC UID",
		},
		{
			name: "terminating Job",
			mutate: func(job *batchv1.Job) {
				now := metav1.Now()
				job.DeletionTimestamp = &now
			},
			wantErr: "is terminating",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			pvc := fixture.boundPVC(true)
			pv := fixture.boundPV(pvc)
			job := fixture.completedWriterJob()
			tt.mutate(job)
			k8sClient := fixture.useK8sObjects(pvc, pv, job)

			state, claimName, err := runRWXReadOnlySetup(t, fixture)
			assert.Equal(t, ModelCachingFailed, state)
			assert.Empty(t, claimName)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assertNoKubernetesWrites(t, k8sClient.Actions())
		})
	}
}

func TestRWXReadOnlyRejectsBoundPVIdentityDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*corev1.PersistentVolumeClaim, *corev1.PersistentVolume)
		omitPV  bool
		wantErr string
	}{
		{
			name: "empty volume name",
			mutate: func(pvc *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
				pvc.Spec.VolumeName = ""
			},
			wantErr: "has no PV name",
		},
		{name: "missing PV", omitPV: true, wantErr: "references missing PV"},
		{
			name: "missing claimRef",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.ClaimRef = nil
			},
			wantErr: "has no claimRef",
		},
		{
			name: "wrong claimRef namespace",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.ClaimRef.Namespace = "foreign-namespace"
			},
			wantErr: "claimRef does not match exact PVC",
		},
		{
			name: "wrong claimRef name",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.ClaimRef.Name = "foreign-claim"
			},
			wantErr: "claimRef does not match exact PVC",
		},
		{
			name: "wrong claimRef UID",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.ClaimRef.UID = types.UID("foreign-pvc-uid")
			},
			wantErr: "claimRef UID",
		},
		{
			name: "wrong CSI driver",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.CSI.Driver = "foreign.csi.example.com"
			},
			wantErr: "CSI driver",
		},
		{
			name: "missing CSI source",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.CSI = nil
			},
			wantErr: "CSI driver",
		},
		{
			name: "wrong access mode",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			},
			wantErr: "access modes",
		},
		{
			name: "wrong StorageClass",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.StorageClassName = "foreign-sc"
			},
			wantErr: "StorageClass",
		},
		{
			name: "wrong reclaim policy",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			},
			wantErr: "reclaim policy",
		},
		{
			name: "empty CSI volume handle",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Spec.CSI.VolumeHandle = ""
			},
			wantErr: "empty CSI volume handle",
		},
		{
			name: "wrong volume mode",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				mode := corev1.PersistentVolumeBlock
				pv.Spec.VolumeMode = &mode
			},
			wantErr: "volume mode",
		},
		{
			name: "terminating PVC",
			mutate: func(pvc *corev1.PersistentVolumeClaim, _ *corev1.PersistentVolume) {
				now := metav1.Now()
				pvc.DeletionTimestamp = &now
			},
			wantErr: "is terminating",
		},
		{
			name: "terminating PV",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				now := metav1.Now()
				pv.DeletionTimestamp = &now
			},
			wantErr: "is terminating",
		},
		{
			name: "PV Available",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Status.Phase = corev1.VolumeAvailable
			},
			wantErr: "phase",
		},
		{
			name: "PV Released",
			mutate: func(_ *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume) {
				pv.Status.Phase = corev1.VolumeReleased
			},
			wantErr: "phase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			pvc := fixture.boundPVC(true)
			pv := fixture.boundPV(pvc)
			job := fixture.completedWriterJob()
			if tt.mutate != nil {
				tt.mutate(pvc, pv)
			}
			objects := []runtime.Object{pvc, job}
			if !tt.omitPV {
				objects = append(objects, pv)
			}
			k8sClient := fixture.useK8sObjects(objects...)

			state, claimName, err := runRWXReadOnlySetup(t, fixture)
			assert.Equal(t, ModelCachingFailed, state)
			assert.Empty(t, claimName)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assertRWXReadOnlyActionTrace(t, k8sClient.Actions(), pvc.Name, fixture.job.Name)
			assertNoKubernetesWrites(t, k8sClient.Actions())
		})
	}
}

func TestRWXReadOnlyRejectsCompletedWriterJobDriftBeforeMarker(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(false)
	pv := fixture.boundPV(pvc)
	job := fixture.completedWriterJob()
	job.Spec.Template.Spec.Containers = []corev1.Container{{
		Name:  "foreign-writer",
		Image: "example.invalid/foreign:latest",
	}}
	k8sClient := fixture.useK8sObjects(pvc, pv, job)

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorContains(t, err, "immutable spec does not match intent")
	assert.True(t, nvcaerrors.IsTerminal(err))
	assertNoKubernetesWrites(t, k8sClient.Actions())

	storedPVC, getErr := k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).
		Get(t.Context(), pvc.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.NotContains(t, storedPVC.Labels, nvcastorage.ModelCachePopulatedLabelKey)
}

func TestRWXReadOnlyRejectsWriterJobTTL(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	ttl := int32(60)
	fixture.job.Spec.TTLSecondsAfterFinished = &ttl

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorContains(t, err, "must not use ttlSecondsAfterFinished")
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.Empty(t, fixture.backend.clients.K8s.(*fakek8sclient.Clientset).Actions())
}

func TestRWXReadOnlyRejectsWriterWithoutWritableClaimMount(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*batchv1.Job)
		wantErr string
	}{
		{
			name: "missing claim volume",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Volumes = nil
			},
			wantErr: "has no",
		},
		{
			name: "different claim",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "other-claim"
			},
			wantErr: "does not reference PVC",
		},
		{
			name: "read-only claim source",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true
			},
			wantErr: "PVC volume",
		},
		{
			name: "read-only mount",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Containers[0].VolumeMounts[0].ReadOnly = true
			},
			wantErr: "does not mount PVC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			tt.mutate(fixture.job)
			state, claimName, err := runRWXReadOnlySetup(t, fixture)
			assert.Equal(t, ModelCachingFailed, state)
			assert.Empty(t, claimName)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assert.Empty(t, fixture.backend.clients.K8s.(*fakek8sclient.Clientset).Actions())
		})
	}
}

func TestRWXReadOnlyRejectsDecoyWritablePVCMount(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	fixture.job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "other-claim"
	fixture.job.Spec.Template.Spec.Volumes = append(
		fixture.job.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "decoy-model-cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fixture.rwPVC.Name,
				},
			},
		})
	fixture.job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		fixture.job.Spec.Template.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{Name: "decoy-model-cache", MountPath: "/decoy"})

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.Error(t, err)
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.Empty(t, fixture.backend.clients.K8s.(*fakek8sclient.Clientset).Actions())
}

func TestRWXReadOnlyCleansUpObjectsWhenBindingRetiresAfterCreate(t *testing.T) {
	fixture := newResolvedWekaRWXReadOnlyRuntimeFixture(t)
	k8sClient := fixture.backend.clients.K8s.(*fakek8sclient.Clientset)
	pvcCreated := false
	jobCreated := false
	k8sClient.Fake.PrependReactor(
		"create", "persistentvolumeclaims",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).
				GetObject().(*corev1.PersistentVolumeClaim).DeepCopy()
			created.UID = types.UID("created-writer-pvc-uid")
			created.ResourceVersion = "created-writer-pvc-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims"),
				created, created.Namespace))
			pvcCreated = true
			return true, created, nil
		})
	k8sClient.Fake.PrependReactor(
		"create", "jobs",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			created := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
			created.UID = types.UID("created-writer-job-uid")
			created.ResourceVersion = "created-writer-job-rv"
			require.NoError(t, k8sClient.Tracker().Create(
				batchv1.SchemeGroupVersion.WithResource("jobs"),
				created, created.Namespace))
			jobCreated = true

			binding, err := fixture.backend.clients.BART.NvcaV2beta1().
				ModelCacheBindings(fixture.binding.Namespace).
				Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
			require.NoError(t, err)
			binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
			_, err = fixture.backend.clients.BART.NvcaV2beta1().
				ModelCacheBindings(binding.Namespace).
				UpdateStatus(t.Context(), binding, metav1.UpdateOptions{})
			require.NoError(t, err)
			return true, created, nil
		})
	k8sClient.ClearActions()

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingFailed, state)
	assert.Empty(t, claimName)
	require.ErrorIs(t, err, errRegularModelCacheBindingRetiring)
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.True(t, pvcCreated)
	assert.True(t, jobCreated)

	_, getErr := k8sClient.CoreV1().PersistentVolumeClaims(fixture.rwPVC.Namespace).
		Get(t.Context(), fixture.rwPVC.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr), "binding-owned PVC must be removed")
	_, getErr = k8sClient.BatchV1().Jobs(fixture.job.Namespace).
		Get(t.Context(), fixture.job.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr), "binding-owned Job must be removed")
	assert.Equal(t, 1, countRWXReadOnlyAction(
		k8sClient.Actions(), "delete", "persistentvolumeclaims"))
	assert.Equal(t, 1, countRWXReadOnlyAction(k8sClient.Actions(), "delete", "jobs"))
}

func TestRWXReadOnlyRetriesPopulatedMarkerConflict(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(false)
	pv := fixture.boundPV(pvc)
	job := fixture.completedWriterJob()
	k8sClient := fixture.useK8sObjects(pvc, pv, job)
	updateAttempts := 0
	k8sClient.Fake.PrependReactor(
		"update", "persistentvolumeclaims",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			updateAttempts++
			if updateAttempts == 1 {
				return true, nil, apierrors.NewConflict(
					corev1.Resource("persistentvolumeclaims"), pvc.Name,
					assert.AnError)
			}
			return false, nil, nil
		})

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	require.NoError(t, err)
	assert.Equal(t, ModelCachingCompleted, state)
	assert.Equal(t, pvc.Name, claimName)
	assert.Equal(t, 2, updateAttempts)
	storedPVC, getErr := k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).
		Get(t.Context(), pvc.Name, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, nvcastorage.ModelCachePopulatedLabelValue,
		storedPVC.Labels[nvcastorage.ModelCachePopulatedLabelKey])
}

func TestRWXReadOnlyRefusesPublicationStateRace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *rwxReadOnlyRuntimeFixture, *fakek8sclient.Clientset,
			*corev1.PersistentVolume) error
		wantErr string
	}{
		{
			name: "PV identity changes",
			mutate: func(_ *testing.T, _ *rwxReadOnlyRuntimeFixture,
				k8sClient *fakek8sclient.Clientset, pv *corev1.PersistentVolume) error {
				drifted := pv.DeepCopy()
				drifted.Spec.CSI.Driver = "foreign.csi.example.com"
				return k8sClient.Tracker().Update(
					corev1.SchemeGroupVersion.WithResource("persistentvolumes"),
					drifted, "")
			},
			wantErr: "CSI driver",
		},
		{
			name: "completed Job disappears",
			mutate: func(_ *testing.T, fixture *rwxReadOnlyRuntimeFixture,
				k8sClient *fakek8sclient.Clientset, _ *corev1.PersistentVolume) error {
				return k8sClient.Tracker().Delete(
					batchv1.SchemeGroupVersion.WithResource("jobs"),
					fixture.job.Namespace, fixture.job.Name)
			},
			wantErr: "publication fence Job",
		},
		{
			name: "binding retires",
			mutate: func(t *testing.T, fixture *rwxReadOnlyRuntimeFixture,
				_ *fakek8sclient.Clientset, _ *corev1.PersistentVolume) error {
				binding, err := fixture.backend.clients.BART.NvcaV2beta1().
					ModelCacheBindings(fixture.binding.Namespace).
					Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
				_, err = fixture.backend.clients.BART.NvcaV2beta1().
					ModelCacheBindings(binding.Namespace).
					UpdateStatus(t.Context(), binding, metav1.UpdateOptions{})
				return err
			},
			wantErr: "Retiring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			pvc := fixture.boundPVC(false)
			pv := fixture.boundPV(pvc)
			job := fixture.completedWriterJob()
			k8sClient := fixture.useK8sObjects(pvc, pv, job)
			raced := false
			k8sClient.Fake.PrependReactor(
				"update", "persistentvolumeclaims",
				func(k8stesting.Action) (bool, runtime.Object, error) {
					if raced {
						return false, nil, nil
					}
					raced = true
					require.NoError(t, tt.mutate(t, fixture, k8sClient, pv))
					return true, nil, apierrors.NewConflict(
						corev1.Resource("persistentvolumeclaims"), pvc.Name, assert.AnError)
				})

			state, claimName, err := runRWXReadOnlySetup(t, fixture)
			assert.Equal(t, ModelCachingFailed, state)
			assert.Empty(t, claimName)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
			storedPVC, getErr := k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).
				Get(t.Context(), pvc.Name, metav1.GetOptions{})
			require.NoError(t, getErr)
			assert.NotContains(t, storedPVC.Labels, nvcastorage.ModelCachePopulatedLabelKey)
		})
	}
}

func TestRWXReadOnlyPropagatesTransientPVRead(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(false)
	pv := fixture.boundPV(pvc)
	k8sClient := fixture.useK8sObjects(pvc, pv)
	k8sClient.Fake.PrependReactor(
		"get", "persistentvolumes",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("PV API unavailable")
		})

	state, claimName, err := runRWXReadOnlySetup(t, fixture)
	assert.Equal(t, ModelCachingInProgress, state)
	assert.Empty(t, claimName)
	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.False(t, nvcaerrors.IsTerminal(err))
	assertNoKubernetesWrites(t, k8sClient.Actions())
}

func TestCleanupModelCachingSetupArtifactsRWXReadOnlyHandlesRetainedPV(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(false)
	pv := fixture.boundPV(pvc)
	job := fixture.writerJob()
	pvc.APIVersion = "v1"
	pvc.Kind = "PersistentVolumeClaim"
	job.APIVersion = "batch/v1"
	job.Kind = "Job"
	setCleanupArtifacts(t, fixture.req, pvc, job)
	k8sClient := fixture.useK8sObjects(pvc, pv, job)

	require.NoError(t, fixture.backend.CleanupModelCachingSetupArtifacts(
		newTestContext(), fixture.req))
	storedBinding, err := fixture.backend.clients.BART.NvcaV2beta1().
		ModelCacheBindings(fixture.binding.Namespace).
		Get(t.Context(), fixture.binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvcav2beta1.ModelCacheBindingPhaseRetiring, storedBinding.Status.Phase)
	updatedPV, err := k8sClient.CoreV1().PersistentVolumes().Get(
		t.Context(), pv.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.PersistentVolumeReclaimDelete,
		updatedPV.Spec.PersistentVolumeReclaimPolicy)
	_, err = k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).
		Get(t.Context(), pvc.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	_, err = k8sClient.BatchV1().Jobs(job.Namespace).
		Get(t.Context(), job.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	pvUpdateIndex, pvcDeleteIndex := -1, -1
	for index, action := range k8sClient.Actions() {
		switch {
		case action.GetVerb() == "update" && action.GetResource().Resource == "persistentvolumes":
			pvUpdateIndex = index
		case action.GetVerb() == "delete" &&
			action.GetResource().Resource == "persistentvolumeclaims":
			pvcDeleteIndex = index
		}
	}
	require.NotEqual(t, -1, pvUpdateIndex)
	require.NotEqual(t, -1, pvcDeleteIndex)
	assert.Less(t, pvUpdateIndex, pvcDeleteIndex,
		"the exact retained PV must change to Delete before the claim is removed")
}

func TestSetupContainerModelCachingRWXReadOnlyMountsSameClaimReadOnly(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	pvc := fixture.boundPVC(true)
	pv := fixture.boundPV(pvc)
	job := fixture.completedWriterJob()
	k8sClient := fixture.useK8sObjects(pvc, pv, job)

	mutate, claimName, err := fixture.backend.setupContainerModelCaching(
		newTestContext(),
		fixture.req,
		fixture.rwPVC.DeepCopy(),
		fixture.job.DeepCopy(),
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, mutate)
	assert.Equal(t, pvc.Name, claimName)

	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Volumes: []corev1.Volume{
			{Name: ModelVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "unrelated", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
		InitContainers: []corev1.Container{{
			Name: "init",
			VolumeMounts: []corev1.VolumeMount{
				{Name: ModelVolumeName, MountPath: "/models"},
				{Name: "unrelated", MountPath: "/config"},
			},
		}},
		Containers: []corev1.Container{{
			Name: "worker",
			VolumeMounts: []corev1.VolumeMount{
				{Name: ModelVolumeName, MountPath: "/models"},
				{Name: "unrelated", MountPath: "/config"},
			},
		}},
	}}
	mutate(pod)

	require.NotNil(t, pod.Spec.Volumes[0].PersistentVolumeClaim)
	assert.Equal(t, pvc.Name, pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
	assert.True(t, pod.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly)
	assert.True(t, pod.Spec.InitContainers[0].VolumeMounts[0].ReadOnly)
	assert.True(t, pod.Spec.Containers[0].VolumeMounts[0].ReadOnly)
	assert.False(t, pod.Spec.InitContainers[0].VolumeMounts[1].ReadOnly)
	assert.False(t, pod.Spec.Containers[0].VolumeMounts[1].ReadOnly)
	assert.NotNil(t, pod.Spec.Volumes[1].EmptyDir)

	assertRWXReadOnlyActionTrace(t, k8sClient.Actions(), pvc.Name, fixture.job.Name)
	assert.Equal(t, 0, countRWXReadOnlyAction(k8sClient.Actions(), "create", "jobs"))
	assert.Equal(t, 0, countRWXReadOnlyAction(k8sClient.Actions(), "delete", "jobs"))
}

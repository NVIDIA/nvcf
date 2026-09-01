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

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var errModelCacheBindingOwnership = errors.New("model cache binding ownership mismatch")

const (
	// ModelCacheBindingUIDLabelKey identifies resources owned by one immutable
	// model-cache binding. Cache-handle labels remain for event fan-out only.
	ModelCacheBindingUIDLabelKey = "nvca.nvcf.nvidia.io/model-cache-binding-uid"
	// ModelCacheRequestUIDLabelKey identifies per-request reader resources. A
	// binding UID is intentionally shared by every request using one cache, so it
	// cannot by itself distinguish a stale same-name reader left by an earlier
	// ICMSRequest generation.
	ModelCacheRequestUIDLabelKey = "nvca.nvcf.nvidia.io/model-cache-request-uid"
)

// SetModelCacheBindingUIDLabel stamps an object with an immutable binding UID.
// It rejects an existing label owned by another binding.
func SetModelCacheBindingUIDLabel(obj metav1.Object, bindingUID types.UID) error {
	if obj == nil {
		return fmt.Errorf("cannot label a nil object with a model cache binding UID")
	}
	value := string(bindingUID)
	if value == "" {
		return fmt.Errorf("model cache binding UID is empty")
	}
	if errs := validation.IsValidLabelValue(value); len(errs) != 0 {
		return fmt.Errorf("model cache binding UID %q is not a valid label value: %v", value, errs)
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	if existing := labels[ModelCacheBindingUIDLabelKey]; existing != "" && existing != value {
		return fmt.Errorf("%w: object %s/%s belongs to model cache binding UID %q, not %q",
			errModelCacheBindingOwnership,
			obj.GetNamespace(), obj.GetName(), existing, value)
	}
	labels[ModelCacheBindingUIDLabelKey] = value
	return nil
}

// ValidateModelCacheBindingUIDLabel requires an object's existing ownership
// label to match the exact binding UID. It never adopts an unlabeled object.
func ValidateModelCacheBindingUIDLabel(obj metav1.Object, bindingUID types.UID) error {
	if obj == nil {
		return fmt.Errorf("cannot validate model cache binding ownership on a nil object")
	}
	want := string(bindingUID)
	if got := obj.GetLabels()[ModelCacheBindingUIDLabelKey]; got != want {
		return fmt.Errorf("%w: object %s/%s has model cache binding UID %q, want %q",
			errModelCacheBindingOwnership,
			obj.GetNamespace(), obj.GetName(), got, want)
	}
	return nil
}

// SetModelCacheRequestUIDLabel stamps a per-request reader object with the
// exact API-assigned ICMSRequest UID. Shared writer objects must not use this
// label because several requests can legitimately share them.
func SetModelCacheRequestUIDLabel(obj metav1.Object, requestUID types.UID) error {
	if obj == nil {
		return fmt.Errorf("cannot label a nil object with a model cache request UID")
	}
	value := string(requestUID)
	if value == "" {
		return fmt.Errorf("model cache request UID is empty")
	}
	if errs := validation.IsValidLabelValue(value); len(errs) != 0 {
		return fmt.Errorf("model cache request UID %q is not a valid label value: %v", value, errs)
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	if existing := labels[ModelCacheRequestUIDLabelKey]; existing != "" && existing != value {
		return fmt.Errorf("%w: object %s/%s belongs to model cache request UID %q, not %q",
			errModelCacheBindingOwnership,
			obj.GetNamespace(), obj.GetName(), existing, value)
	}
	labels[ModelCacheRequestUIDLabelKey] = value
	return nil
}

// ValidateModelCacheRequestUIDLabel requires exact per-request ownership and
// never adopts an unlabeled reader object.
func ValidateModelCacheRequestUIDLabel(obj metav1.Object, requestUID types.UID) error {
	if obj == nil {
		return fmt.Errorf("cannot validate model cache request ownership on a nil object")
	}
	want := string(requestUID)
	if got := obj.GetLabels()[ModelCacheRequestUIDLabelKey]; got != want {
		return fmt.Errorf("%w: object %s/%s has model cache request UID %q, want %q",
			errModelCacheBindingOwnership,
			obj.GetNamespace(), obj.GetName(), got, want)
	}
	return nil
}

// ValidateModelCacheReaderOwnership is shared by reader reconciliation and
// cleanup. Both the shared binding identity and the exact request generation
// must match before a per-request PV or PVC can be adopted or deleted.
func ValidateModelCacheReaderOwnership(
	obj metav1.Object,
	bindingUID types.UID,
	requestUID types.UID,
) error {
	if err := ValidateModelCacheBindingUIDLabel(obj, bindingUID); err != nil {
		return err
	}
	return ValidateModelCacheRequestUIDLabel(obj, requestUID)
}

func propagateModelCacheBindingUIDLabel(owner metav1.Object, objects ...metav1.Object) error {
	if owner == nil {
		return fmt.Errorf("model cache binding label owner is nil")
	}
	bindingUID := types.UID(owner.GetLabels()[ModelCacheBindingUIDLabelKey])
	if bindingUID == "" {
		return nil
	}
	for _, obj := range objects {
		if err := SetModelCacheBindingUIDLabel(obj, bindingUID); err != nil {
			return err
		}
	}
	return nil
}

func validateHelmModelCacheWriterObjectMeta(
	existing metav1.Object,
	wanted metav1.Object,
	bindingUID types.UID,
) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf("%w: Helm model cache writer object identity is incomplete",
			errModelCacheBindingOwnership)
	}
	if existing.GetNamespace() != wanted.GetNamespace() || existing.GetName() != wanted.GetName() {
		return fmt.Errorf("%w: Helm model cache writer object %s/%s does not match %s/%s",
			errModelCacheBindingOwnership,
			existing.GetNamespace(), existing.GetName(), wanted.GetNamespace(), wanted.GetName())
	}
	if err := ValidateModelCacheBindingUIDLabel(existing, bindingUID); err != nil {
		return err
	}
	for key, want := range wanted.GetLabels() {
		if existing.GetLabels()[key] != want {
			return fmt.Errorf("%w: Helm model cache writer object %s/%s label %q is %q, want %q",
				errModelCacheBindingOwnership, existing.GetNamespace(), existing.GetName(),
				key, existing.GetLabels()[key], want)
		}
	}
	return nil
}

func modelCachePVCVolumeMode(pvc *corev1.PersistentVolumeClaim) corev1.PersistentVolumeMode {
	if pvc != nil && pvc.Spec.VolumeMode != nil {
		return *pvc.Spec.VolumeMode
	}
	return corev1.PersistentVolumeFilesystem
}

func validateHelmModelCacheWriterPVC(
	existing *corev1.PersistentVolumeClaim,
	wanted *corev1.PersistentVolumeClaim,
	bindingUID types.UID,
) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf("%w: Helm model cache writer PVC intent is incomplete",
			errModelCacheBindingOwnership)
	}
	if err := validateHelmModelCacheWriterObjectMeta(existing, wanted, bindingUID); err != nil {
		return err
	}
	if !slices.Equal(existing.Spec.AccessModes, wanted.Spec.AccessModes) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.StorageClassName, wanted.Spec.StorageClassName) ||
		modelCachePVCVolumeMode(existing) != modelCachePVCVolumeMode(wanted) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.Resources, wanted.Spec.Resources) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.Selector, wanted.Spec.Selector) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.DataSource, wanted.Spec.DataSource) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.DataSourceRef, wanted.Spec.DataSourceRef) ||
		!apiequality.Semantic.DeepEqual(
			existing.Spec.VolumeAttributesClassName, wanted.Spec.VolumeAttributesClassName) {
		return fmt.Errorf("%w: Helm model cache writer PVC %s/%s immutable spec does not match intent",
			errModelCacheBindingOwnership, existing.Namespace, existing.Name)
	}
	return nil
}

func normalizeHelmWriterContainerDefaults(container *corev1.Container) {
	if container == nil {
		return
	}
	defaultPullPolicy := corev1.PullIfNotPresent
	image := container.Image
	lastSlash := -1
	for i := len(image) - 1; i >= 0; i-- {
		if image[i] == '/' {
			lastSlash = i
			break
		}
	}
	lastColon := -1
	for i := len(image) - 1; i > lastSlash; i-- {
		if image[i] == ':' {
			lastColon = i
			break
		}
	}
	if lastColon < 0 || image[lastColon+1:] == "latest" {
		defaultPullPolicy = corev1.PullAlways
	}
	if container.ImagePullPolicy == defaultPullPolicy {
		container.ImagePullPolicy = ""
	}
	if container.TerminationMessagePath == corev1.TerminationMessagePathDefault {
		container.TerminationMessagePath = ""
	}
	if container.TerminationMessagePolicy == corev1.TerminationMessageReadFile {
		container.TerminationMessagePolicy = ""
	}
	for i := range container.Ports {
		if container.Ports[i].Protocol == corev1.ProtocolTCP {
			container.Ports[i].Protocol = ""
		}
	}
}

func normalizeHelmWriterPodSpec(spec *corev1.PodSpec) {
	if spec == nil {
		return
	}
	if spec.DNSPolicy == corev1.DNSClusterFirst {
		spec.DNSPolicy = ""
	}
	if spec.SchedulerName == corev1.DefaultSchedulerName {
		spec.SchedulerName = ""
	}
	if spec.TerminationGracePeriodSeconds != nil && *spec.TerminationGracePeriodSeconds == 30 {
		spec.TerminationGracePeriodSeconds = nil
	}
	if spec.EnableServiceLinks != nil && *spec.EnableServiceLinks {
		spec.EnableServiceLinks = nil
	}
	if spec.PreemptionPolicy != nil && *spec.PreemptionPolicy == corev1.PreemptLowerPriority {
		spec.PreemptionPolicy = nil
	}
	for i := range spec.Containers {
		normalizeHelmWriterContainerDefaults(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		normalizeHelmWriterContainerDefaults(&spec.InitContainers[i])
	}
	for i := range spec.Volumes {
		volume := &spec.Volumes[i]
		defaultModes := make([]**int32, 0, 4)
		if volume.Secret != nil {
			defaultModes = append(defaultModes, &volume.Secret.DefaultMode)
		}
		if volume.ConfigMap != nil {
			defaultModes = append(defaultModes, &volume.ConfigMap.DefaultMode)
		}
		if volume.DownwardAPI != nil {
			defaultModes = append(defaultModes, &volume.DownwardAPI.DefaultMode)
		}
		if volume.Projected != nil {
			defaultModes = append(defaultModes, &volume.Projected.DefaultMode)
		}
		for _, defaultMode := range defaultModes {
			if *defaultMode != nil && **defaultMode == 0o644 {
				*defaultMode = nil
			}
		}
	}
}

func normalizeHelmWriterJobSpec(spec *batchv1.JobSpec) {
	if spec == nil {
		return
	}
	if spec.Parallelism != nil && *spec.Parallelism == 1 {
		spec.Parallelism = nil
	}
	if spec.Completions != nil && *spec.Completions == 1 {
		spec.Completions = nil
	}
	if spec.BackoffLimit != nil && *spec.BackoffLimit == 6 {
		spec.BackoffLimit = nil
	}
	if spec.CompletionMode != nil && *spec.CompletionMode == batchv1.NonIndexedCompletion {
		spec.CompletionMode = nil
	}
	if spec.Suspend != nil && !*spec.Suspend {
		spec.Suspend = nil
	}
	if spec.ManualSelector != nil && !*spec.ManualSelector {
		spec.ManualSelector = nil
	}
}

func validateHelmModelCacheWriterJob(
	existing *batchv1.Job,
	wanted *batchv1.Job,
	bindingUID types.UID,
) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf("%w: Helm model cache writer Job intent is incomplete",
			errModelCacheBindingOwnership)
	}
	if err := validateHelmModelCacheWriterObjectMeta(existing, wanted, bindingUID); err != nil {
		return err
	}
	if err := ValidateModelCacheBindingUIDLabel(&existing.Spec.Template.ObjectMeta, bindingUID); err != nil {
		return fmt.Errorf("writer Job Pod template: %w", err)
	}
	for key, want := range wanted.Spec.Template.Labels {
		if existing.Spec.Template.Labels[key] != want {
			return fmt.Errorf("%w: Helm model cache writer Job %s/%s Pod-template label %q is %q, want %q",
				errModelCacheBindingOwnership, existing.Namespace, existing.Name,
				key, existing.Spec.Template.Labels[key], want)
		}
	}
	for key, want := range wanted.Spec.Template.Annotations {
		if existing.Spec.Template.Annotations[key] != want {
			return fmt.Errorf("%w: Helm model cache writer Job %s/%s Pod-template annotation %q changed",
				errModelCacheBindingOwnership, existing.Namespace, existing.Name, key)
		}
	}

	existingSpec := existing.Spec.DeepCopy()
	wantedSpec := wanted.Spec.DeepCopy()
	existingTemplate := existingSpec.Template.Spec.DeepCopy()
	wantedTemplate := wantedSpec.Template.Spec.DeepCopy()
	normalizeHelmWriterPodSpec(existingTemplate)
	normalizeHelmWriterPodSpec(wantedTemplate)
	existingSpec.Template = corev1.PodTemplateSpec{}
	wantedSpec.Template = corev1.PodTemplateSpec{}
	if wantedSpec.Selector == nil {
		existingSpec.Selector = nil
	}
	normalizeHelmWriterJobSpec(existingSpec)
	normalizeHelmWriterJobSpec(wantedSpec)
	if !apiequality.Semantic.DeepEqual(existingSpec, wantedSpec) ||
		!apiequality.Semantic.DeepEqual(existingTemplate, wantedTemplate) {
		return fmt.Errorf("%w: Helm model cache writer Job %s/%s immutable spec does not match intent",
			errModelCacheBindingOwnership, existing.Namespace, existing.Name)
	}
	return nil
}

func validateHelmModelCacheWriterLease(
	existing *coordv1.Lease,
	wanted *coordv1.Lease,
	bindingUID types.UID,
) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf("%w: Helm model cache writer Lease intent is incomplete",
			errModelCacheBindingOwnership)
	}
	if err := validateHelmModelCacheWriterObjectMeta(existing, wanted, bindingUID); err != nil {
		return err
	}
	if existing.Spec.LeaseDurationSeconds == nil || wanted.Spec.LeaseDurationSeconds == nil ||
		*existing.Spec.LeaseDurationSeconds != *wanted.Spec.LeaseDurationSeconds {
		return fmt.Errorf("%w: Helm model cache writer Lease %s/%s duration does not match intent",
			errModelCacheBindingOwnership, existing.Namespace, existing.Name)
	}
	if existing.Spec.HolderIdentity == nil || *existing.Spec.HolderIdentity == "" {
		return fmt.Errorf("%w: Helm model cache writer Lease %s/%s has no holder",
			errModelCacheBindingOwnership, existing.Namespace, existing.Name)
	}
	return nil
}

func modelCacheSecretData(secret *corev1.Secret) map[string][]byte {
	if secret == nil {
		return nil
	}
	data := make(map[string][]byte, len(secret.Data)+len(secret.StringData))
	for key, value := range secret.Data {
		data[key] = append([]byte(nil), value...)
	}
	for key, value := range secret.StringData {
		data[key] = []byte(value)
	}
	return data
}

func validateHelmModelCacheWriterSecret(
	existing *corev1.Secret,
	wanted *corev1.Secret,
	bindingUID types.UID,
) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf("%w: Helm model cache writer Secret intent is incomplete",
			errModelCacheBindingOwnership)
	}
	if err := validateHelmModelCacheWriterObjectMeta(existing, wanted, bindingUID); err != nil {
		return err
	}
	for key, want := range wanted.Annotations {
		if existing.Annotations[key] != want {
			return fmt.Errorf("%w: Helm model cache writer Secret %s/%s annotation %q changed",
				errModelCacheBindingOwnership, existing.Namespace, existing.Name, key)
		}
	}
	if existing.Type != wanted.Type ||
		!apiequality.Semantic.DeepEqual(existing.Immutable, wanted.Immutable) ||
		!apiequality.Semantic.DeepEqual(modelCacheSecretData(existing), modelCacheSecretData(wanted)) {
		return fmt.Errorf("%w: Helm model cache writer Secret %s/%s immutable intent does not match",
			errModelCacheBindingOwnership, existing.Namespace, existing.Name)
	}
	return nil
}

func validateHelmModelCacheBindingOwnedObjectIntent(
	existing client.Object,
	wanted client.Object,
	bindingUID types.UID,
) error {
	switch wanted := wanted.(type) {
	case *corev1.PersistentVolumeClaim:
		got, ok := existing.(*corev1.PersistentVolumeClaim)
		if !ok {
			return fmt.Errorf("%w: existing Helm writer object %T is not a PVC",
				errModelCacheBindingOwnership, existing)
		}
		return validateHelmModelCacheWriterPVC(got, wanted, bindingUID)
	case *batchv1.Job:
		got, ok := existing.(*batchv1.Job)
		if !ok {
			return fmt.Errorf("%w: existing Helm writer object %T is not a Job",
				errModelCacheBindingOwnership, existing)
		}
		return validateHelmModelCacheWriterJob(got, wanted, bindingUID)
	case *coordv1.Lease:
		got, ok := existing.(*coordv1.Lease)
		if !ok {
			return fmt.Errorf("%w: existing Helm writer object %T is not a Lease",
				errModelCacheBindingOwnership, existing)
		}
		return validateHelmModelCacheWriterLease(got, wanted, bindingUID)
	case *corev1.Secret:
		got, ok := existing.(*corev1.Secret)
		if !ok {
			return fmt.Errorf("%w: existing Helm writer object %T is not a Secret",
				errModelCacheBindingOwnership, existing)
		}
		return validateHelmModelCacheWriterSecret(got, wanted, bindingUID)
	default:
		return ValidateModelCacheBindingUIDLabel(existing, bindingUID)
	}
}

// prepareHelmModelCacheBindingResources proves that translated writer
// artifacts and the ownership Lease match the immutable binding intent before
// any create. Existing shared objects are accepted only with the exact binding
// UID; per-request ownership is deliberately not applied to shared writers.
func (r *Reconciler) prepareHelmModelCacheBindingResources(
	ctx context.Context,
	binding *nvcav2beta1.ModelCacheBinding,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	lease *coordv1.Lease,
) error {
	if binding == nil {
		return fmt.Errorf("model cache binding is nil")
	}
	if rwPVC == nil || initJob == nil || lease == nil {
		return fmt.Errorf("Helm model cache writer PVC, Job, and Lease must be present")
	}
	resources := binding.Spec.Resources
	if resources.WriterNamespace != ModelCacheInitNamespace {
		return fmt.Errorf("%w: Helm model cache writer namespace %q is not %q",
			errModelCacheBindingOwnership, resources.WriterNamespace, ModelCacheInitNamespace)
	}
	if len(resources.PersistentVolumeClaimNames) != 1 ||
		resources.PersistentVolumeClaimNames[0] != rwPVC.Name {
		return fmt.Errorf("%w: Helm model cache writer PVC %q does not match binding intent %v",
			errModelCacheBindingOwnership, rwPVC.Name, resources.PersistentVolumeClaimNames)
	}
	if len(resources.JobNames) != 1 || resources.JobNames[0] != initJob.Name {
		return fmt.Errorf("%w: Helm model cache writer Job %q does not match binding intent %v",
			errModelCacheBindingOwnership, initJob.Name, resources.JobNames)
	}
	if resources.LeaseName == "" || resources.LeaseName != lease.Name {
		return fmt.Errorf("%w: Helm model cache Lease %q does not match binding intent %q",
			errModelCacheBindingOwnership, lease.Name, resources.LeaseName)
	}
	for kind, obj := range map[string]metav1.Object{
		"writer PVC": rwPVC,
		"writer Job": initJob,
		"Lease":      lease,
	} {
		if obj.GetNamespace() != resources.WriterNamespace {
			return fmt.Errorf("%w: Helm model cache %s namespace %q does not match binding intent %q",
				errModelCacheBindingOwnership, kind, obj.GetNamespace(), resources.WriterNamespace)
		}
		if err := ValidateModelCacheBindingUIDLabel(obj, binding.UID); err != nil {
			return err
		}
	}
	if err := ValidateModelCacheBindingUIDLabel(&initJob.Spec.Template.ObjectMeta, binding.UID); err != nil {
		return fmt.Errorf("writer Job Pod template: %w", err)
	}

	for _, wanted := range []client.Object{rwPVC, initJob, lease} {
		existing, ok := wanted.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("model cache object %T is not a controller-runtime client object", wanted)
		}
		err := r.Client.Get(ctx, client.ObjectKeyFromObject(wanted), existing)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return fmt.Errorf("get existing Helm model cache object %s/%s: %w",
				wanted.GetNamespace(), wanted.GetName(), err)
		default:
			if err := validateHelmModelCacheBindingOwnedObjectIntent(
				existing, wanted, binding.UID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHelmModelCacheSecondaryPV(
	st *nvcav1new.StorageRequest,
	selection *PersistedModelCacheStorageSelection,
	primaryPV *corev1.PersistentVolume,
	secondaryPV *corev1.PersistentVolume,
	roPVCName string,
	bindingUID types.UID,
	requestUID types.UID,
) error {
	if st == nil || st.Spec.ModelCache == nil || selection == nil || primaryPV == nil || secondaryPV == nil {
		return fmt.Errorf("%w: incomplete Helm model cache secondary PV identity",
			errModelCacheBindingOwnership)
	}
	if err := ValidateModelCacheReaderOwnership(secondaryPV, bindingUID, requestUID); err != nil {
		return err
	}
	if err := ValidateModelCacheBindingUIDLabel(primaryPV, bindingUID); err != nil {
		return err
	}
	expectedName := "secondary-pv-" + st.Spec.ICMSRequestName
	if secondaryPV.Name != expectedName || secondaryPV.Namespace != "" {
		return fmt.Errorf("%w: secondary PV identity %q/%q does not match %q",
			errModelCacheBindingOwnership, secondaryPV.Namespace, secondaryPV.Name, expectedName)
	}
	if primaryPV.Spec.CSI == nil || secondaryPV.Spec.CSI == nil {
		return fmt.Errorf("%w: primary or secondary PV has no CSI source", errModelCacheBindingOwnership)
	}
	if primaryPV.Spec.CSI.Driver != selection.Provisioner || secondaryPV.Spec.CSI.Driver != selection.Provisioner {
		return fmt.Errorf("%w: primary/secondary PV provisioner does not match persisted provisioner %q",
			errModelCacheBindingOwnership, selection.Provisioner)
	}
	expectedHandle, err := updateSecondaryPVVolumeHandle(primaryPV.Spec.CSI.VolumeHandle, st.Namespace)
	if err != nil {
		return fmt.Errorf("%w: derive secondary PV volume identity: %w", errModelCacheBindingOwnership, err)
	}
	if secondaryPV.Spec.CSI.VolumeHandle != expectedHandle {
		return fmt.Errorf("%w: secondary PV volume identity %q does not match %q",
			errModelCacheBindingOwnership, secondaryPV.Spec.CSI.VolumeHandle, expectedHandle)
	}
	if secondaryPV.Spec.StorageClassName != primaryPV.Spec.StorageClassName {
		return fmt.Errorf("%w: secondary PV StorageClass %q does not match primary PV %q",
			errModelCacheBindingOwnership, secondaryPV.Spec.StorageClassName, primaryPV.Spec.StorageClassName)
	}
	expectedCSI := primaryPV.Spec.CSI.DeepCopy()
	expectedCSI.VolumeHandle = expectedHandle
	if !apiequality.Semantic.DeepEqual(secondaryPV.Spec.CSI, expectedCSI) {
		return fmt.Errorf("%w: secondary PV CSI source does not match the primary PV intent",
			errModelCacheBindingOwnership)
	}
	if secondaryPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf("%w: secondary PV %q reclaim policy is %q, want Retain",
			errModelCacheBindingOwnership, secondaryPV.Name,
			secondaryPV.Spec.PersistentVolumeReclaimPolicy)
	}
	if !apiequality.Semantic.DeepEqual(secondaryPV.Spec.Capacity, primaryPV.Spec.Capacity) ||
		!apiequality.Semantic.DeepEqual(secondaryPV.Spec.VolumeMode, primaryPV.Spec.VolumeMode) ||
		!apiequality.Semantic.DeepEqual(secondaryPV.Spec.NodeAffinity, primaryPV.Spec.NodeAffinity) {
		return fmt.Errorf("%w: secondary PV %q capacity, volume mode, or node affinity changed",
			errModelCacheBindingOwnership, secondaryPV.Name)
	}
	if !slices.Equal(secondaryPV.Spec.AccessModes, accessModesRO) {
		return fmt.Errorf("%w: secondary PV access modes %v do not match %v",
			errModelCacheBindingOwnership, secondaryPV.Spec.AccessModes, accessModesRO)
	}
	claimRef := secondaryPV.Spec.ClaimRef
	if claimRef == nil || claimRef.APIVersion != "v1" || claimRef.Kind != "PersistentVolumeClaim" ||
		claimRef.Namespace != st.Namespace || claimRef.Name != roPVCName {
		return fmt.Errorf("%w: secondary PV claimRef does not match reader PVC %s/%s",
			errModelCacheBindingOwnership, st.Namespace, roPVCName)
	}
	return nil
}

func validateHelmModelCacheReaderPVC(
	st *nvcav1new.StorageRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	secondaryPV *corev1.PersistentVolume,
	roPVC *corev1.PersistentVolumeClaim,
	bindingUID types.UID,
	requestUID types.UID,
) error {
	if st == nil || st.Spec.ModelCache == nil || rwPVC == nil || secondaryPV == nil || roPVC == nil {
		return fmt.Errorf("%w: incomplete Helm model cache reader PVC identity",
			errModelCacheBindingOwnership)
	}
	if err := ValidateModelCacheReaderOwnership(roPVC, bindingUID, requestUID); err != nil {
		return err
	}
	expectedName := "ro-pvc-" + st.Spec.ModelCache.CacheHandle
	expectedPVName := "secondary-pv-" + st.Spec.ICMSRequestName
	if roPVC.Name != expectedName || roPVC.Namespace != st.Namespace {
		return fmt.Errorf("%w: reader PVC identity %s/%s does not match %s/%s",
			errModelCacheBindingOwnership, roPVC.Namespace, roPVC.Name, st.Namespace, expectedName)
	}
	if roPVC.Spec.VolumeName != expectedPVName || secondaryPV.Name != expectedPVName {
		return fmt.Errorf("%w: reader PVC volumeName %q does not match secondary PV %q",
			errModelCacheBindingOwnership, roPVC.Spec.VolumeName, expectedPVName)
	}
	if !slices.Equal(roPVC.Spec.AccessModes, accessModesRO) {
		return fmt.Errorf("%w: reader PVC access modes %v do not match %v",
			errModelCacheBindingOwnership, roPVC.Spec.AccessModes, accessModesRO)
	}
	if (roPVC.Spec.StorageClassName == nil) != (rwPVC.Spec.StorageClassName == nil) ||
		(roPVC.Spec.StorageClassName != nil && *roPVC.Spec.StorageClassName != *rwPVC.Spec.StorageClassName) {
		return fmt.Errorf("%w: reader PVC StorageClass does not match writer PVC",
			errModelCacheBindingOwnership)
	}
	claimRef := secondaryPV.Spec.ClaimRef
	if claimRef == nil || claimRef.Namespace != roPVC.Namespace || claimRef.Name != roPVC.Name {
		return fmt.Errorf("%w: secondary PV claimRef does not match reader PVC identity",
			errModelCacheBindingOwnership)
	}
	if roPVC.Status.Phase == corev1.ClaimBound && (roPVC.UID == "" || claimRef.UID != roPVC.UID) {
		return fmt.Errorf("%w: bound reader PVC %s/%s UID %q does not match secondary PV claimRef UID %q",
			errModelCacheBindingOwnership, roPVC.Namespace, roPVC.Name, roPVC.UID, claimRef.UID)
	}
	if roPVC.Status.Phase != corev1.ClaimBound && claimRef.UID != "" && claimRef.UID != roPVC.UID {
		return fmt.Errorf("%w: secondary PV claimRef UID %q does not match reader PVC UID %q",
			errModelCacheBindingOwnership, claimRef.UID, roPVC.UID)
	}
	return nil
}

// validateHelmModelCachePrimaryPVForReuse proves that a discovered primary PV
// is the exact immutable backing volume finalized for this binding. In
// particular, a matching cache-handle label alone is not authority to mutate
// or clone a PV.
func validateHelmModelCachePrimaryPVForReuse(
	st *nvcav1new.StorageRequest,
	selection *PersistedModelCacheStorageSelection,
	rwPVC *corev1.PersistentVolumeClaim,
	primaryPV *corev1.PersistentVolume,
	bindingUID types.UID,
) error {
	if st == nil || st.Spec.ModelCache == nil || selection == nil || rwPVC == nil || primaryPV == nil {
		return fmt.Errorf("%w: incomplete Helm model cache primary PV identity",
			errModelCacheBindingOwnership)
	}
	if err := ValidateModelCacheBindingUIDLabel(primaryPV, bindingUID); err != nil {
		return err
	}
	if primaryPV.Labels[primaryPVLabelKey] != primaryPVLabelValue ||
		primaryPV.Labels[modelCacheHandleLabelKey] != st.Spec.ModelCache.CacheHandle {
		return fmt.Errorf("%w: primary PV %q labels do not match cache handle %q",
			errModelCacheBindingOwnership, primaryPV.Name, st.Spec.ModelCache.CacheHandle)
	}
	if primaryPV.Spec.CSI == nil || primaryPV.Spec.CSI.Driver != selection.Provisioner {
		return fmt.Errorf("%w: primary PV %q provisioner does not match persisted provisioner %q",
			errModelCacheBindingOwnership, primaryPV.Name, selection.Provisioner)
	}
	if primaryPV.Spec.CSI.VolumeHandle == "" {
		return fmt.Errorf("%w: primary PV %q has an empty volume handle",
			errModelCacheBindingOwnership, primaryPV.Name)
	}
	expectedHandle, err := updateSecondaryPVVolumeHandle(
		primaryPV.Spec.CSI.VolumeHandle, ModelCacheInitNamespace)
	if err != nil || expectedHandle != primaryPV.Spec.CSI.VolumeHandle {
		return fmt.Errorf("%w: primary PV %q volume handle %q does not identify writer namespace %q",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.CSI.VolumeHandle, ModelCacheInitNamespace)
	}
	if !selection.EncryptionRequired && primaryPV.Spec.StorageClassName != selection.StorageClassName {
		return fmt.Errorf("%w: primary PV %q StorageClass %q does not match persisted StorageClass %q",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.StorageClassName, selection.StorageClassName)
	}
	if primaryPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf("%w: primary PV %q reclaim policy is %q, want Retain",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.PersistentVolumeReclaimPolicy)
	}
	if !slices.Equal(primaryPV.Spec.AccessModes,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) {
		return fmt.Errorf("%w: primary PV %q access modes %v do not match writer RWO intent",
			errModelCacheBindingOwnership, primaryPV.Name, primaryPV.Spec.AccessModes)
	}
	claimRef := primaryPV.Spec.ClaimRef
	if claimRef == nil || claimRef.APIVersion != "v1" || claimRef.Kind != "PersistentVolumeClaim" ||
		claimRef.Namespace != ModelCacheInitNamespace || claimRef.Name != rwPVC.Name || claimRef.UID == "" {
		return fmt.Errorf("%w: primary PV %q claimRef does not identify writer PVC %s/%s",
			errModelCacheBindingOwnership, primaryPV.Name, ModelCacheInitNamespace, rwPVC.Name)
	}
	if rwPVC.UID != "" && claimRef.UID != rwPVC.UID {
		return fmt.Errorf("%w: primary PV %q claimRef UID %q does not match live writer PVC UID %q",
			errModelCacheBindingOwnership, primaryPV.Name, claimRef.UID, rwPVC.UID)
	}
	return nil
}

func validateHelmModelCachePrimaryPVForFinalize(
	selection *PersistedModelCacheStorageSelection,
	rwPVC *corev1.PersistentVolumeClaim,
	primaryPV *corev1.PersistentVolume,
) error {
	if selection == nil || rwPVC == nil || primaryPV == nil || primaryPV.Spec.CSI == nil {
		return fmt.Errorf("%w: primary PV provisioning identity is incomplete",
			errModelCacheBindingOwnership)
	}
	if primaryPV.Spec.CSI.Driver != selection.Provisioner {
		return fmt.Errorf("%w: primary PV %q provisioner %q does not match persisted provisioner %q",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.CSI.Driver, selection.Provisioner)
	}
	if primaryPV.Spec.CSI.VolumeHandle == "" {
		return fmt.Errorf("%w: primary PV %q has an empty volume handle",
			errModelCacheBindingOwnership, primaryPV.Name)
	}
	expectedHandle, err := updateSecondaryPVVolumeHandle(
		primaryPV.Spec.CSI.VolumeHandle, ModelCacheInitNamespace)
	if err != nil || expectedHandle != primaryPV.Spec.CSI.VolumeHandle {
		return fmt.Errorf("%w: primary PV %q volume handle %q does not identify writer namespace %q",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.CSI.VolumeHandle, ModelCacheInitNamespace)
	}
	if rwPVC.Spec.StorageClassName == nil || *rwPVC.Spec.StorageClassName == "" ||
		primaryPV.Spec.StorageClassName != *rwPVC.Spec.StorageClassName {
		return fmt.Errorf("%w: primary PV %q StorageClass %q does not match writer PVC",
			errModelCacheBindingOwnership, primaryPV.Name, primaryPV.Spec.StorageClassName)
	}
	if primaryPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf("%w: primary PV %q reclaim policy is %q, want Retain",
			errModelCacheBindingOwnership, primaryPV.Name, primaryPV.Spec.PersistentVolumeReclaimPolicy)
	}
	if !slices.Equal(primaryPV.Spec.AccessModes, rwPVC.Spec.AccessModes) {
		return fmt.Errorf("%w: primary PV %q access modes %v do not match writer PVC %v",
			errModelCacheBindingOwnership, primaryPV.Name,
			primaryPV.Spec.AccessModes, rwPVC.Spec.AccessModes)
	}
	return nil
}

func validateHelmModelCachePrimaryPVClaim(
	rwPVC *corev1.PersistentVolumeClaim,
	primaryPV *corev1.PersistentVolume,
	bindingUID types.UID,
) error {
	if rwPVC == nil || primaryPV == nil {
		return fmt.Errorf("%w: writer PVC and primary PV must be present", errModelCacheBindingOwnership)
	}
	if rwPVC.Namespace != ModelCacheInitNamespace || rwPVC.UID == "" {
		return fmt.Errorf("%w: writer PVC %s/%s has no exact API identity",
			errModelCacheBindingOwnership, rwPVC.Namespace, rwPVC.Name)
	}
	if err := ValidateModelCacheBindingUIDLabel(rwPVC, bindingUID); err != nil {
		return err
	}
	if rwPVC.Spec.VolumeName != primaryPV.Name {
		return fmt.Errorf("%w: writer PVC volumeName %q does not match primary PV %q",
			errModelCacheBindingOwnership, rwPVC.Spec.VolumeName, primaryPV.Name)
	}
	claimRef := primaryPV.Spec.ClaimRef
	if claimRef == nil || claimRef.APIVersion != "v1" || claimRef.Kind != "PersistentVolumeClaim" ||
		claimRef.Namespace != rwPVC.Namespace || claimRef.Name != rwPVC.Name || claimRef.UID != rwPVC.UID {
		return fmt.Errorf("%w: primary PV claimRef does not match writer PVC %s/%s UID %q",
			errModelCacheBindingOwnership, rwPVC.Namespace, rwPVC.Name, rwPVC.UID)
	}
	return nil
}

func (r *Reconciler) createOrValidateModelCacheBindingOwnedObject(
	ctx context.Context,
	obj client.Object,
) (bool, error) {
	err := r.Client.Create(ctx, obj)
	if err == nil {
		return false, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}
	bindingUID := types.UID(obj.GetLabels()[ModelCacheBindingUIDLabelKey])
	if bindingUID == "" {
		return true, nil
	}
	existing, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return true, fmt.Errorf("model cache object %T is not a controller-runtime client object", obj)
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		return true, fmt.Errorf("get existing model cache object %s/%s: %w",
			obj.GetNamespace(), obj.GetName(), err)
	}
	if err := validateHelmModelCacheBindingOwnedObjectIntent(existing, obj, bindingUID); err != nil {
		return true, err
	}
	return true, nil
}

func (r *Reconciler) validatePersistedHelmModelCacheBinding(
	ctx context.Context,
	st *nvcav1new.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
	selection *PersistedModelCacheStorageSelection,
) (*nvcav2beta1.ModelCacheBinding, error) {
	if st == nil || st.Spec.ModelCache == nil {
		return nil, fmt.Errorf("model cache StorageRequest is missing its modelCache spec")
	}
	if icmsReq == nil {
		return nil, fmt.Errorf("ICMSRequest is nil")
	}
	if selection == nil {
		return nil, fmt.Errorf("persisted model cache storage selection is nil")
	}
	if selection.Workflow != ModelCacheWorkflowHelm || selection.Mode != ModelCacheSelectionDurable {
		return nil, fmt.Errorf("model cache binding requires a durable Helm selection")
	}
	if selection.BindingName == "" || selection.BindingUID == "" {
		return nil, fmt.Errorf("durable Helm model cache selection has no binding reference")
	}
	if icmsReq.UID == "" {
		return nil, fmt.Errorf("ICMSRequest %s/%s has no UID", icmsReq.Namespace, icmsReq.Name)
	}
	if st.Spec.ICMSRequestName != icmsReq.Name || st.Spec.ICMSRequestNamespace != icmsReq.Namespace {
		return nil, fmt.Errorf("StorageRequest ICMS identity %s/%s does not match %s/%s",
			st.Spec.ICMSRequestNamespace, st.Spec.ICMSRequestName, icmsReq.Namespace, icmsReq.Name)
	}
	if got := st.Annotations[ICMSRequestUIDAnnotationKey]; got != string(icmsReq.UID) {
		return nil, fmt.Errorf("StorageRequest ICMS UID %q does not match %q", got, icmsReq.UID)
	}

	binding := &nvcav2beta1.ModelCacheBinding{}
	key := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: selection.BindingName}
	if err := r.Client.Get(ctx, key, binding); err != nil {
		return nil, fmt.Errorf("get model cache binding %s/%s: %w", key.Namespace, key.Name, err)
	}
	if binding.UID != selection.BindingUID {
		return nil, fmt.Errorf("model cache binding %s/%s UID %q does not match persisted UID %q",
			binding.Namespace, binding.Name, binding.UID, selection.BindingUID)
	}
	if err := ValidateModelCacheBinding(
		binding,
		selection,
		icmsReq.Spec.NCAId,
		st.Spec.ModelCache.CacheHandle,
		ModelCacheInitNamespace,
	); err != nil {
		return nil, err
	}
	if !ModelCacheBindingHasRequestReference(binding, icmsReq.Namespace, icmsReq.Name, icmsReq.UID) {
		return nil, fmt.Errorf("model cache binding %s/%s has no reference to ICMSRequest %s/%s UID %q",
			binding.Namespace, binding.Name, icmsReq.Namespace, icmsReq.Name, icmsReq.UID)
	}
	return binding, nil
}

func (r *Reconciler) validateModelCacheBindingForCleanup(
	ctx context.Context,
	st *nvcav1new.StorageRequest,
) (types.UID, bool, bool, error) {
	raw := st.Annotations[ModelCacheStorageSelectionAnnotationKey]
	if raw == "" {
		return "", false, false, nil
	}
	selection, err := ParsePersistedModelCacheStorageSelection(raw)
	if err != nil {
		return "", true, false, fmt.Errorf("parse persisted model cache storage selection for cleanup: %w", err)
	}
	if selection.Workflow != ModelCacheWorkflowHelm || selection.Mode != ModelCacheSelectionDurable ||
		selection.BindingName == "" || selection.BindingUID == "" {
		return "", true, false, fmt.Errorf("annotated model cache cleanup requires a durable Helm binding reference")
	}
	requestUID := types.UID(st.Annotations[ICMSRequestUIDAnnotationKey])
	if requestUID == "" || st.Spec.ICMSRequestName == "" || st.Spec.ICMSRequestNamespace == "" {
		return "", true, false, fmt.Errorf("annotated model cache cleanup has an incomplete ICMSRequest identity")
	}
	sharingDomain, ok := nvcatypes.GetNCAIDLabelVal(st.Labels)
	if !ok {
		return "", true, false, fmt.Errorf("annotated model cache cleanup has no sharing-domain label")
	}

	binding := &nvcav2beta1.ModelCacheBinding{}
	key := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: selection.BindingName}
	if err := r.Client.Get(ctx, key, binding); err != nil {
		return "", true, false, fmt.Errorf("get model cache binding for cleanup %s/%s: %w", key.Namespace, key.Name, err)
	}
	if err := ValidateModelCacheBinding(
		binding, selection, sharingDomain, st.Spec.ModelCache.CacheHandle, ModelCacheInitNamespace); err != nil {
		return "", true, false, fmt.Errorf("refusing model cache cleanup: %w", err)
	}
	referencePresent := ModelCacheBindingHasRequestReference(
		binding, st.Spec.ICMSRequestNamespace, st.Spec.ICMSRequestName, requestUID)
	if !referencePresent {
		// The ICMSRequest finalizer releases its binding reference before the
		// request disappears. The persisted StorageRequest identity is the
		// cleanup tombstone, but it is valid only after the exact request is
		// deleting or gone. A live request without its reference fails closed.
		request := &nvcav2beta1.ICMSRequest{}
		requestKey := client.ObjectKey{
			Namespace: st.Spec.ICMSRequestNamespace,
			Name:      st.Spec.ICMSRequestName,
		}
		err := r.Client.Get(ctx, requestKey, request)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return "", true, false, fmt.Errorf(
				"verify ICMSRequest deletion before model cache cleanup %s/%s: %w",
				requestKey.Namespace, requestKey.Name, err)
		case request.UID != requestUID:
			return "", true, false, fmt.Errorf(
				"refusing model cache cleanup: live ICMSRequest %s/%s UID %q does not match recorded UID %q",
				request.Namespace, request.Name, request.UID, requestUID)
		case request.DeletionTimestamp.IsZero():
			return "", true, false, fmt.Errorf(
				"refusing model cache cleanup: live ICMSRequest %s/%s has no exact binding reference",
				request.Namespace, request.Name)
		}
	}
	return binding.UID, true, referencePresent, nil
}

func (r *Reconciler) validateModelCacheInitCleanupOwnership(
	ctx context.Context,
	cacheHandle string,
	bindingUID types.UID,
	holderIdentity string,
) (bool, *coordv1.Lease, error) {
	listOpts := []client.ListOption{
		client.MatchingLabels(map[string]string{modelCacheHandleLabelKey: cacheHandle}),
		client.InNamespace(ModelCacheInitNamespace),
	}

	jobs := &batchv1.JobList{}
	if err := r.Client.List(ctx, jobs, listOpts...); err != nil {
		return false, nil, fmt.Errorf("list model cache Jobs before cleanup: %w", err)
	}
	pods := &corev1.PodList{}
	if err := r.Client.List(ctx, pods, listOpts...); err != nil {
		return false, nil, fmt.Errorf("list model cache Pods before cleanup: %w", err)
	}
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcs, listOpts...); err != nil {
		return false, nil, fmt.Errorf("list model cache PVCs before cleanup: %w", err)
	}
	if len(jobs.Items) > 1 {
		return false, nil, fmt.Errorf("refusing model cache cleanup: found %d writer Jobs for cache handle %q",
			len(jobs.Items), cacheHandle)
	}
	if len(jobs.Items) == 1 && jobs.Items[0].Name != "writer-job-"+cacheHandle {
		return false, nil, fmt.Errorf("refusing model cache cleanup: writer Job %q does not match cache handle %q",
			jobs.Items[0].Name, cacheHandle)
	}
	if len(pvcs.Items) > 1 {
		return false, nil, fmt.Errorf("refusing model cache cleanup: found %d writer PVCs for cache handle %q",
			len(pvcs.Items), cacheHandle)
	}
	if len(pvcs.Items) == 1 && pvcs.Items[0].Name != "rw-pvc-"+cacheHandle {
		return false, nil, fmt.Errorf("refusing model cache cleanup: writer PVC %q does not match cache handle %q",
			pvcs.Items[0].Name, cacheHandle)
	}
	secrets := &corev1.SecretList{}
	if err := r.Client.List(ctx, secrets, listOpts...); err != nil {
		return false, nil, fmt.Errorf("list model cache Secrets before cleanup: %w", err)
	}

	lease := &coordv1.Lease{}
	key := client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: buildInitLeaseName(cacheHandle)}
	if err := r.Client.Get(ctx, key, lease); err != nil {
		if apierrors.IsNotFound(err) {
			if len(jobs.Items)+len(pods.Items)+len(pvcs.Items)+len(secrets.Items) != 0 {
				return false, nil, fmt.Errorf("refusing model cache cleanup: writer artifacts exist without their ownership Lease")
			}
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("get model cache Lease before cleanup: %w", err)
	}
	if err := ValidateModelCacheBindingUIDLabel(lease, bindingUID); err != nil {
		return false, nil, err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return false, nil, fmt.Errorf("refusing model cache cleanup: ownership Lease has no holder")
	}
	if *lease.Spec.HolderIdentity != holderIdentity {
		return false, lease, nil
	}

	for i := range jobs.Items {
		if err := ValidateModelCacheBindingUIDLabel(&jobs.Items[i], bindingUID); err != nil {
			return false, nil, err
		}
	}
	for i := range pods.Items {
		if err := ValidateModelCacheBindingUIDLabel(&pods.Items[i], bindingUID); err != nil {
			return false, nil, err
		}
	}
	for i := range pvcs.Items {
		if err := ValidateModelCacheBindingUIDLabel(&pvcs.Items[i], bindingUID); err != nil {
			return false, nil, err
		}
	}
	for i := range secrets.Items {
		if err := ValidateModelCacheBindingUIDLabel(&secrets.Items[i], bindingUID); err != nil {
			return false, nil, err
		}
	}
	return true, lease, nil
}

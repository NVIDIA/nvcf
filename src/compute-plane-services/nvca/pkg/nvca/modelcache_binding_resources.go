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
	"context"
	"fmt"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// regularModelCacheTargetClaim names the claim whose lifecycle drives regular
// model caching, and reports whether a reader claim is created separately.
//
// The two shapes run the same machine over different claims. The ROX shape
// gives readers their own claim, derived from the writer volume, so that claim
// is what setup waits on. Every other shape publishes the writer claim itself
// and readers mount it read-only, so there is no second claim to look for.
//
// Callers must not derive a reader claim name before this: the name is
// meaningless for a shape that has no reader claim, and deriving it can fail
// for a request that would never have used it.
func regularModelCacheTargetClaim(
	binding *nvcav2beta1.ModelCacheBinding, writerPVCName string,
) (name string, separateReader bool, err error) {
	if binding == nil {
		// No binding means the legacy annotation-free path, which is ROX only.
		readerName, nameErr := regularModelCacheReaderPVCName(writerPVCName)
		return readerName, true, nameErr
	}
	_, _, separateReader, err = regularModelCacheAccessModePlan(binding)
	if err != nil {
		return "", false, err
	}
	if !separateReader {
		return writerPVCName, false, nil
	}
	readerName, err := regularModelCacheReaderPVCName(writerPVCName)
	return readerName, true, err
}

func (c K8sComputeBackend) prepareRegularModelCacheBindingResources(
	ctx context.Context,
	binding *nvcav2beta1.ModelCacheBinding,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
) error {
	if binding == nil {
		return nvcaerrors.TerminalError(fmt.Errorf("regular model cache binding is nil"))
	}
	if rwPVC == nil || initJob == nil {
		return nvcaerrors.TerminalError(fmt.Errorf("regular model cache PVC or init Job is nil"))
	}
	roPVCName, err := regularModelCacheReaderPVCName(rwPVC.Name)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}
	writerModes, _, separateReader, err := regularModelCacheAccessModePlan(binding)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}
	if !slices.Contains(binding.Spec.Resources.PersistentVolumeClaimNames, rwPVC.Name) {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"regular model cache writer PVC name %q does not match binding intent %v",
			rwPVC.Name, binding.Spec.Resources.PersistentVolumeClaimNames))
	}
	if separateReader != slices.Contains(binding.Spec.Resources.PersistentVolumeClaimNames, roPVCName) {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"regular model cache reader PVC name %q does not match transition %q intent %v",
			roPVCName, binding.Spec.Decision.Transition,
			binding.Spec.Resources.PersistentVolumeClaimNames))
	}
	if !slices.Contains(binding.Spec.Resources.JobNames, initJob.Name) {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"regular model cache init Job name %q does not match binding intent %v",
			initJob.Name, binding.Spec.Resources.JobNames))
	}
	for kind, namespace := range map[string]string{
		"PVC": rwPVC.Namespace,
		"Job": initJob.Namespace,
	} {
		if namespace != binding.Spec.Resources.WriterNamespace {
			return nvcaerrors.TerminalError(fmt.Errorf(
				"regular model cache %s namespace %q does not match binding writer namespace %q",
				kind, namespace, binding.Spec.Resources.WriterNamespace))
		}
	}
	for _, obj := range []metav1.Object{rwPVC, initJob, &initJob.Spec.Template.ObjectMeta} {
		canonicalizeRegularModelCacheSharedMetadata(obj)
		if err := nvcastorage.SetModelCacheBindingUIDLabel(obj, binding.UID); err != nil {
			return nvcaerrors.TerminalError(err)
		}
	}
	if !slices.Equal(rwPVC.Spec.AccessModes, writerModes) {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"regular model cache writer PVC access modes are %v, want %v for transition %q",
			rwPVC.Spec.AccessModes, writerModes, binding.Spec.Decision.Transition))
	}

	pvcTargets := []struct {
		wanted *corev1.PersistentVolumeClaim
		reader bool
	}{
		{wanted: rwPVC},
	}
	if separateReader {
		roPVCIntent := rwPVC.DeepCopy()
		roPVCIntent.Name = roPVCName
		roPVCIntent.Spec.AccessModes = ROAccessMode
		pvcTargets = append(pvcTargets, struct {
			wanted *corev1.PersistentVolumeClaim
			reader bool
		}{wanted: roPVCIntent, reader: true})
	}
	for _, target := range pvcTargets {
		if err := validateRegularModelCachePVC(target.wanted, target.wanted, binding, target.reader); err != nil {
			return nvcaerrors.TerminalError(err)
		}
	}
	if err := validateRegularModelCacheJob(initJob, initJob, binding); err != nil {
		return nvcaerrors.TerminalError(err)
	}

	for _, target := range pvcTargets {
		existing, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(
			binding.Spec.Resources.WriterNamespace).Get(ctx, target.wanted.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return fmt.Errorf("get existing regular model cache PVC %s/%s: %w",
				binding.Spec.Resources.WriterNamespace, target.wanted.Name, err)
		default:
			if err := validateRegularModelCachePVC(existing, target.wanted, binding, target.reader); err != nil {
				return nvcaerrors.TerminalError(err)
			}
		}
	}
	existingJob, err := c.clients.K8s.BatchV1().Jobs(binding.Spec.Resources.WriterNamespace).
		Get(ctx, initJob.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("get existing regular model cache Job %s/%s: %w",
			binding.Spec.Resources.WriterNamespace, initJob.Name, err)
	default:
		if err := validateRegularModelCacheJob(existingJob, initJob, binding); err != nil {
			return nvcaerrors.TerminalError(err)
		}
	}
	return nil
}

func regularModelCacheBindingUID(
	req *nvcav2beta1.ICMSRequest,
) (types.UID, bool, error) {
	selection, err := persistedRegularModelCacheSelection(req)
	if err != nil {
		return "", false, err
	}
	if selection == nil || selection.Mode != nvcastorage.ModelCacheSelectionDurable {
		return "", false, nil
	}
	if selection.BindingName == "" || selection.BindingUID == "" {
		return "", true, fmt.Errorf("durable regular model cache selection has no binding reference")
	}
	return selection.BindingUID, true, nil
}

func requireRegularModelCacheBindingUID(obj metav1.Object, bindingUID types.UID) error {
	if obj == nil {
		return fmt.Errorf("regular model cache object is nil")
	}
	if got := obj.GetLabels()[nvcastorage.ModelCacheBindingUIDLabelKey]; got != string(bindingUID) {
		return fmt.Errorf("regular model cache object %s/%s has binding UID %q, want %q",
			obj.GetNamespace(), obj.GetName(), got, bindingUID)
	}
	return nil
}

func regularModelCacheExpectedStorageClassName(binding *nvcav2beta1.ModelCacheBinding) (string, error) {
	if binding == nil {
		return "", fmt.Errorf("regular model cache binding is nil")
	}
	if binding.Spec.Decision.EncryptionRequired {
		if len(binding.Spec.Resources.StorageClassNames) != 1 {
			return "", fmt.Errorf("encrypted regular model cache binding must record exactly one derived StorageClass")
		}
		return binding.Spec.Resources.StorageClassNames[0], nil
	}
	if binding.Spec.StorageClass.Name == "" {
		return "", fmt.Errorf("regular model cache binding has no selected StorageClass")
	}
	return binding.Spec.StorageClass.Name, nil
}

func regularModelCacheAccessModePlan(
	binding *nvcav2beta1.ModelCacheBinding,
) (
	writer []corev1.PersistentVolumeAccessMode,
	reader []corev1.PersistentVolumeAccessMode,
	separateReader bool,
	err error,
) {
	if binding == nil {
		return nil, nil, false, fmt.Errorf("regular model cache binding is nil")
	}
	switch binding.Spec.Decision.Transition {
	case nvcastorage.ModelCacheTransitionROXReadOnly:
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			append([]corev1.PersistentVolumeAccessMode(nil), ROAccessMode...), true, nil
	case nvcastorage.ModelCacheTransitionRWXReadOnly:
		return append([]corev1.PersistentVolumeAccessMode(nil), RWXAccessMode...), nil, false, nil
	default:
		return nil, nil, false, fmt.Errorf(
			"unsupported regular model cache transition %q", binding.Spec.Decision.Transition)
	}
}

func regularModelCacheExpectedPVCModes(
	binding *nvcav2beta1.ModelCacheBinding,
	reader bool,
) ([]corev1.PersistentVolumeAccessMode, error) {
	writerModes, readerModes, separateReader, err := regularModelCacheAccessModePlan(binding)
	if err != nil {
		return nil, err
	}
	if !reader {
		return writerModes, nil
	}
	if !separateReader {
		return nil, fmt.Errorf(
			"regular model cache transition %q has no separate reader PVC",
			binding.Spec.Decision.Transition)
	}
	return readerModes, nil
}

func regularModelCachePVCVolumeMode(pvc *corev1.PersistentVolumeClaim) corev1.PersistentVolumeMode {
	if pvc != nil && pvc.Spec.VolumeMode != nil {
		return *pvc.Spec.VolumeMode
	}
	return corev1.PersistentVolumeFilesystem
}

func regularModelCachePVVolumeMode(pv *corev1.PersistentVolume) corev1.PersistentVolumeMode {
	if pv != nil && pv.Spec.VolumeMode != nil {
		return *pv.Spec.VolumeMode
	}
	return corev1.PersistentVolumeFilesystem
}

func regularModelCacheRequestLabelKeys() []string {
	return []string{
		nvcatypes.ICMSRequestIDKey,
		nvcatypes.NCAIDKey,
		nvcatypes.NCAIDUpperKey,
		nvcatypes.MessageBatchIDKey,
		nvcatypes.GPUNameKey,
		nvcatypes.FunctionIDKey,
		nvcatypes.FunctionIDUpperKey,
		nvcatypes.FunctionVersionIDKey,
		nvcatypes.FunctionVersionIDUpperKey,
		nvcatypes.TaskIDKey,
		nvcatypes.TaskIDUpperKey,
		nvcatypes.ShaderCacheLabelKey,
	}
}

func regularModelCacheRequestAnnotationKeys() []string {
	return []string{
		nvcatypes.ICMSRequestIDKey,
		nvcatypes.NCAIDKey,
		nvcatypes.ClusterGroupKey,
		nvcatypes.InstanceCountKey,
	}
}

func canonicalizeRegularModelCacheSharedMetadata(obj metav1.Object) {
	if obj == nil {
		return
	}
	obj.SetOwnerReferences(nil)
	labels := obj.GetLabels()
	for _, key := range regularModelCacheRequestLabelKeys() {
		delete(labels, key)
	}
	obj.SetLabels(labels)
	annotations := obj.GetAnnotations()
	for _, key := range regularModelCacheRequestAnnotationKeys() {
		delete(annotations, key)
	}
	obj.SetAnnotations(annotations)
}

func validateRegularModelCacheSharedMetadata(obj metav1.Object) error {
	if obj == nil {
		return fmt.Errorf("regular model cache object is nil")
	}
	if len(obj.GetOwnerReferences()) != 0 {
		return fmt.Errorf("regular model cache object %s/%s has request owner references",
			obj.GetNamespace(), obj.GetName())
	}
	for _, key := range regularModelCacheRequestLabelKeys() {
		if _, found := obj.GetLabels()[key]; found {
			return fmt.Errorf("regular model cache object %s/%s retains request-scoped label %q",
				obj.GetNamespace(), obj.GetName(), key)
		}
	}
	for _, key := range regularModelCacheRequestAnnotationKeys() {
		if _, found := obj.GetAnnotations()[key]; found {
			return fmt.Errorf("regular model cache object %s/%s retains request-scoped annotation %q",
				obj.GetNamespace(), obj.GetName(), key)
		}
	}
	return nil
}

func bindRegularModelCacheWriterJobToPVC(
	job *batchv1.Job,
	pvc *corev1.PersistentVolumeClaim,
	binding *nvcav2beta1.ModelCacheBinding,
) error {
	if binding == nil || binding.Spec.Decision.Transition !=
		nvcastorage.ModelCacheTransitionRWXReadOnly {
		return nil
	}
	if job == nil || pvc == nil || pvc.UID == "" {
		return fmt.Errorf("rwxReadOnly writer Job cannot record an empty PVC UID")
	}
	if job.Spec.Template.Annotations == nil {
		job.Spec.Template.Annotations = map[string]string{}
	}
	recorded := job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey]
	if recorded != "" && recorded != string(pvc.UID) {
		return fmt.Errorf("rwxReadOnly writer Job records PVC UID %q, want %q",
			recorded, pvc.UID)
	}
	job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey] = string(pvc.UID)
	return nil
}
func validateRegularModelCacheObjectMeta(existing, wanted metav1.Object, bindingUID types.UID) error {
	if existing == nil || wanted == nil {
		return fmt.Errorf(
			"regular model cache object intent is incomplete")
	}
	if err := validateRegularModelCacheSharedMetadata(existing); err != nil {
		return err
	}
	if err := validateRegularModelCacheSharedMetadata(wanted); err != nil {
		return fmt.Errorf("regular model cache intended metadata is not binding-scoped: %w", err)
	}
	if existing.GetNamespace() != wanted.GetNamespace() || existing.GetName() != wanted.GetName() {
		return fmt.Errorf(
			"regular model cache object %s/%s does not match intended %s/%s",
			existing.GetNamespace(), existing.GetName(), wanted.GetNamespace(), wanted.GetName())
	}
	if err := requireRegularModelCacheBindingUID(existing, bindingUID); err != nil {
		return err
	}
	for key, want := range wanted.GetLabels() {
		if existing.GetLabels()[key] != want {
			return fmt.Errorf(
				"regular model cache object %s/%s label %q is %q, want %q",
				existing.GetNamespace(), existing.GetName(), key, existing.GetLabels()[key], want)
		}
	}
	for key, want := range wanted.GetAnnotations() {
		if existing.GetAnnotations()[key] != want {
			return fmt.Errorf(
				"regular model cache object %s/%s annotation %q changed",
				existing.GetNamespace(), existing.GetName(), key)
		}
	}
	return nil
}

func validateRegularModelCachePVC(
	existing *corev1.PersistentVolumeClaim,
	wanted *corev1.PersistentVolumeClaim,
	binding *nvcav2beta1.ModelCacheBinding,
	reader bool,
) error {
	if existing == nil || wanted == nil || binding == nil {
		return fmt.Errorf("regular model cache PVC intent is incomplete")
	}
	if err := validateRegularModelCacheObjectMeta(existing, wanted, binding.UID); err != nil {
		return err
	}
	expectedStorageClass, err := regularModelCacheExpectedStorageClassName(binding)
	if err != nil {
		return err
	}
	if wanted.Spec.StorageClassName == nil || *wanted.Spec.StorageClassName != expectedStorageClass {
		return fmt.Errorf(
			"regular model cache PVC %s/%s intent StorageClass is not %q",
			wanted.Namespace, wanted.Name, expectedStorageClass)
	}
	expectedModes, err := regularModelCacheExpectedPVCModes(binding, reader)
	if err != nil {
		return err
	}
	if !slices.Equal(wanted.Spec.AccessModes, expectedModes) {
		return fmt.Errorf(
			"regular model cache PVC %s/%s intent access modes are %v, want %v",
			wanted.Namespace, wanted.Name, wanted.Spec.AccessModes, expectedModes)
	}
	if !slices.Equal(existing.Spec.AccessModes, wanted.Spec.AccessModes) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.StorageClassName, wanted.Spec.StorageClassName) ||
		regularModelCachePVCVolumeMode(existing) != regularModelCachePVCVolumeMode(wanted) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.Resources, wanted.Spec.Resources) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.Selector, wanted.Spec.Selector) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.DataSource, wanted.Spec.DataSource) ||
		!apiequality.Semantic.DeepEqual(existing.Spec.DataSourceRef, wanted.Spec.DataSourceRef) ||
		!apiequality.Semantic.DeepEqual(
			existing.Spec.VolumeAttributesClassName, wanted.Spec.VolumeAttributesClassName) {
		return fmt.Errorf(
			"regular model cache PVC %s/%s immutable spec does not match intent",
			existing.Namespace, existing.Name)
	}
	if wanted.Spec.VolumeName != "" && existing.Spec.VolumeName != wanted.Spec.VolumeName {
		return fmt.Errorf("regular model cache PVC %s/%s volumeName %q does not match intent %q",
			existing.Namespace, existing.Name, existing.Spec.VolumeName, wanted.Spec.VolumeName)
	}
	if reader && existing.Status.Phase == corev1.ClaimBound && existing.Spec.VolumeName == "" {
		return fmt.Errorf("bound regular model cache reader PVC %s/%s has no volumeName",
			existing.Namespace, existing.Name)
	}
	if reader && binding.Status.Realized != nil &&
		binding.Status.Realized.BoundPersistentVolumeName != "" &&
		existing.Spec.VolumeName != binding.Status.Realized.BoundPersistentVolumeName {

		return fmt.Errorf("regular model cache reader PVC %s/%s volumeName %q does not match binding PV %q",
			existing.Namespace, existing.Name, existing.Spec.VolumeName,
			binding.Status.Realized.BoundPersistentVolumeName)
	}
	return nil
}

func normalizeRegularModelCacheContainerDefaults(container *corev1.Container) {
	if container == nil {
		return
	}
	defaultPullPolicy := corev1.PullIfNotPresent
	lastSlash := strings.LastIndex(container.Image, "/")
	lastColon := strings.LastIndex(container.Image, ":")
	if lastColon <= lastSlash || container.Image[lastColon+1:] == "latest" {
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

func normalizeRegularModelCachePodSpec(spec *corev1.PodSpec) {
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
		normalizeRegularModelCacheContainerDefaults(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		normalizeRegularModelCacheContainerDefaults(&spec.InitContainers[i])
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
func normalizeRegularModelCacheJobSpec(spec *batchv1.JobSpec) {
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

func validateRegularModelCacheJob(
	existing *batchv1.Job,
	wanted *batchv1.Job,
	binding *nvcav2beta1.ModelCacheBinding,
) error {
	if existing == nil || wanted == nil || binding == nil {
		return fmt.Errorf("regular model cache Job intent is incomplete")
	}
	if err := validateRegularModelCacheObjectMeta(existing, wanted, binding.UID); err != nil {
		return err
	}
	if err := validateRegularModelCacheSharedMetadata(&existing.Spec.Template.ObjectMeta); err != nil {
		return fmt.Errorf("regular model cache writer Job Pod template: %w", err)
	}
	if err := validateRegularModelCacheSharedMetadata(&wanted.Spec.Template.ObjectMeta); err != nil {
		return fmt.Errorf("regular model cache intended writer Job Pod template is not binding-scoped: %w", err)
	}
	if err := requireRegularModelCacheBindingUID(&existing.Spec.Template.ObjectMeta, binding.UID); err != nil {
		return fmt.Errorf("regular model cache writer Job Pod template: %w", err)
	}
	for key, want := range wanted.Spec.Template.Labels {
		if existing.Spec.Template.Labels[key] != want {
			return fmt.Errorf("regular model cache Job %s/%s Pod-template label %q changed",
				existing.Namespace, existing.Name, key)
		}
	}
	for key, want := range wanted.Spec.Template.Annotations {
		if existing.Spec.Template.Annotations[key] != want {
			return fmt.Errorf("regular model cache Job %s/%s Pod-template annotation %q changed",
				existing.Namespace, existing.Name, key)
		}
	}

	existingSpec := existing.Spec.DeepCopy()
	wantedSpec := wanted.Spec.DeepCopy()
	existingTemplate := existingSpec.Template.Spec.DeepCopy()
	wantedTemplate := wantedSpec.Template.Spec.DeepCopy()
	normalizeRegularModelCachePodSpec(existingTemplate)
	normalizeRegularModelCachePodSpec(wantedTemplate)
	existingSpec.Template = corev1.PodTemplateSpec{}
	wantedSpec.Template = corev1.PodTemplateSpec{}
	if wantedSpec.Selector == nil {
		existingSpec.Selector = nil
	}
	normalizeRegularModelCacheJobSpec(existingSpec)
	normalizeRegularModelCacheJobSpec(wantedSpec)
	if !apiequality.Semantic.DeepEqual(existingSpec, wantedSpec) ||
		!apiequality.Semantic.DeepEqual(existingTemplate, wantedTemplate) {
		return fmt.Errorf("regular model cache Job %s/%s immutable spec does not match intent",
			existing.Namespace, existing.Name)
	}
	return nil
}

func validateRegularModelCachePVIdentity(
	binding *nvcav2beta1.ModelCacheBinding,
	pv *corev1.PersistentVolume,
	expectedModes []corev1.PersistentVolumeAccessMode,
) error {
	if binding == nil || pv == nil {
		return fmt.Errorf("regular model cache PV identity is incomplete")
	}
	if binding.Spec.Decision.Transition == nvcastorage.ModelCacheTransitionROXReadOnly {
		if err := requireRegularModelCacheBindingUID(pv, binding.UID); err != nil {
			return err
		}
	}
	expectedStorageClass, err := regularModelCacheExpectedStorageClassName(binding)
	if err != nil {
		return err
	}
	if pv.Spec.StorageClassName != expectedStorageClass {
		return fmt.Errorf(
			"regular model cache PV %s StorageClass is %q, want %q",
			pv.Name, pv.Spec.StorageClassName, expectedStorageClass)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf(
			"regular model cache PV %s reclaim policy is %q, want Retain",
			pv.Name, pv.Spec.PersistentVolumeReclaimPolicy)
	}
	if !slices.Equal(pv.Spec.AccessModes, expectedModes) {
		return fmt.Errorf(
			"regular model cache PV %s access modes are %v, want %v",
			pv.Name, pv.Spec.AccessModes, expectedModes)
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != binding.Spec.Decision.Provisioner {
		return fmt.Errorf(
			"regular model cache PV %s CSI driver does not match persisted provisioner %q",
			pv.Name, binding.Spec.Decision.Provisioner)
	}
	if pv.Spec.CSI.VolumeHandle == "" {
		return fmt.Errorf("regular model cache PV %s has an empty CSI volume handle", pv.Name)
	}
	return nil
}

func validateRegularModelCacheReaderPVMountOptions(
	binding *nvcav2beta1.ModelCacheBinding,
	pv *corev1.PersistentVolume,
) error {
	if binding == nil || pv == nil {
		return fmt.Errorf("regular model cache reader mount-option identity is incomplete")
	}
	if binding.Spec.Decision.Transition != nvcastorage.ModelCacheTransitionROXReadOnly {
		return nil
	}
	for _, required := range binding.Spec.Decision.RequiredMountOptions {
		if !slices.Contains(pv.Spec.MountOptions, required) {
			return fmt.Errorf("regular model cache reader PV %s is missing required mount option %q",
				pv.Name, required)
		}
		for _, actual := range pv.Spec.MountOptions {
			if regularModelCacheMountOptionsConflict(required, actual) {
				return fmt.Errorf(
					"regular model cache reader PV %s mount option %q conflicts with required option %q",
					pv.Name, actual, required)
			}
		}
	}
	return nil
}

func validateRegularModelCachePVForPVC(
	binding *nvcav2beta1.ModelCacheBinding,
	pvc *corev1.PersistentVolumeClaim,
	pv *corev1.PersistentVolume,
	expectedModes []corev1.PersistentVolumeAccessMode,
) error {
	if pvc == nil || pvc.UID == "" {
		return fmt.Errorf("regular model cache PVC identity is incomplete")
	}
	if err := validateRegularModelCachePVIdentity(binding, pv, expectedModes); err != nil {
		return err
	}
	if regularModelCachePVVolumeMode(pv) != regularModelCachePVCVolumeMode(pvc) {
		return fmt.Errorf("regular model cache PV %s volume mode %q does not match PVC %s/%s volume mode %q",
			pv.Name, regularModelCachePVVolumeMode(pv),
			pvc.Namespace, pvc.Name, regularModelCachePVCVolumeMode(pvc))
	}
	expectedStorageClass, err := regularModelCacheExpectedStorageClassName(binding)
	if err != nil {
		return err
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != expectedStorageClass {
		return fmt.Errorf("regular model cache PVC %s/%s StorageClass does not match %q",
			pvc.Namespace, pvc.Name, expectedStorageClass)
	}
	claimRef := pv.Spec.ClaimRef
	if claimRef == nil {
		return fmt.Errorf("regular model cache PV %s has no claimRef", pv.Name)
	}
	if claimRef.Namespace != pvc.Namespace || claimRef.Name != pvc.Name {
		return fmt.Errorf("regular model cache PV %s claimRef does not match exact PVC %s/%s UID %q",
			pv.Name, pvc.Namespace, pvc.Name, pvc.UID)
	}
	if claimRef.UID != pvc.UID {
		return fmt.Errorf("regular model cache PV %s claimRef UID %q does not match PVC UID %q",
			pv.Name, claimRef.UID, pvc.UID)
	}
	return nil
}

type regularModelCacheCleanupTargets struct {
	namespace string
	binding   *nvcav2beta1.ModelCacheBinding
	rwPVC     *corev1.PersistentVolumeClaim
	roPVC     *corev1.PersistentVolumeClaim
	initJob   *batchv1.Job
}

func (c K8sComputeBackend) regularModelCacheCleanupBinding(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (*nvcav2beta1.ModelCacheBinding, bool, error) {
	selection, err := persistedRegularModelCacheSelection(req)
	if err != nil {
		return nil, false, fmt.Errorf("parse regular model cache selection for cleanup: %w", err)
	}
	if selection == nil {
		return nil, true, nil
	}
	if selection.Mode != nvcastorage.ModelCacheSelectionDurable {
		return nil, false, nil
	}
	binding, authorized, err := c.bk8s.beginRegularModelCacheBindingRetirement(ctx, req)
	if err != nil {
		return nil, false, err
	}
	return binding, authorized, nil
}

func (c K8sComputeBackend) regularModelCacheTransitionTargets(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVCName string,
	initJobName string,
) (*regularModelCacheCleanupTargets, bool, error) {
	selection, err := persistedRegularModelCacheSelection(req)
	if err != nil {
		return nil, false, fmt.Errorf("parse regular model cache selection for transition: %w", err)
	}
	if selection == nil || selection.Mode != nvcastorage.ModelCacheSelectionDurable {
		return nil, false, nil
	}
	binding, err := c.bk8s.activeModelCacheBindingForRuntime(ctx, req)
	if err != nil {
		return nil, true, err
	}
	targets, err := c.validateRegularModelCacheCleanupTargets(
		ctx, binding, rwPVCName, initJobName)
	if err != nil {
		return nil, true, err
	}
	return targets, true, nil
}

func (c K8sComputeBackend) validateRegularModelCacheCleanupTargets(
	ctx context.Context,
	binding *nvcav2beta1.ModelCacheBinding,
	rwPVCName string,
	initJobName string,
) (*regularModelCacheCleanupTargets, error) {
	if binding == nil {
		return nil, nil
	}
	_, _, separateReader, err := regularModelCacheAccessModePlan(binding)
	if err != nil {
		return nil, err
	}
	roPVCName, err := regularModelCacheReaderPVCName(rwPVCName)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(binding.Spec.Resources.PersistentVolumeClaimNames, rwPVCName) ||
		!slices.Contains(binding.Spec.Resources.JobNames, initJobName) {
		return nil, fmt.Errorf("refusing regular model cache cleanup outside binding resource intent")
	}
	if separateReader != slices.Contains(binding.Spec.Resources.PersistentVolumeClaimNames, roPVCName) {
		return nil, fmt.Errorf(
			"refusing regular model cache cleanup with reader inventory outside transition %q intent",
			binding.Spec.Decision.Transition)
	}
	targets := &regularModelCacheCleanupTargets{
		namespace: binding.Spec.Resources.WriterNamespace,
		binding:   binding,
	}
	pvcTargets := []struct {
		name string
		set  func(*corev1.PersistentVolumeClaim)
	}{
		{name: rwPVCName, set: func(pvc *corev1.PersistentVolumeClaim) { targets.rwPVC = pvc }},
	}
	if separateReader {
		pvcTargets = append(pvcTargets, struct {
			name string
			set  func(*corev1.PersistentVolumeClaim)
		}{name: roPVCName, set: func(pvc *corev1.PersistentVolumeClaim) { targets.roPVC = pvc }})
	}
	for _, target := range pvcTargets {
		pvc, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(binding.Spec.Resources.WriterNamespace).
			Get(ctx, target.name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return nil, fmt.Errorf("get regular model cache cleanup PVC %s/%s: %w",
				binding.Spec.Resources.WriterNamespace, target.name, err)
		default:
			if err := requireRegularModelCacheBindingUID(pvc, binding.UID); err != nil {
				return nil, err
			}
			target.set(pvc)
		}
	}
	job, err := c.clients.K8s.BatchV1().Jobs(binding.Spec.Resources.WriterNamespace).
		Get(ctx, initJobName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("get regular model cache cleanup Job %s/%s: %w",
			binding.Spec.Resources.WriterNamespace, initJobName, err)
	default:
		if err := requireRegularModelCacheBindingUID(job, binding.UID); err != nil {
			return nil, err
		}
		targets.initJob = job
	}
	return targets, nil
}

func validateRegularModelCacheCleanupPV(
	binding *nvcav2beta1.ModelCacheBinding,
	pvc *corev1.PersistentVolumeClaim,
	pv *corev1.PersistentVolume,
) error {
	if binding == nil || pvc == nil || pv == nil {
		return fmt.Errorf("regular model cache cleanup binding, PVC, and PV must be present")
	}
	reader, err := classifyRegularModelCachePVCName(pvc.Name)
	if err != nil {
		return fmt.Errorf("regular model cache cleanup PVC %s/%s is outside writer/reader intent: %w",
			pvc.Namespace, pvc.Name, err)
	}
	expectedModes, err := regularModelCacheExpectedPVCModes(binding, reader)
	if err != nil {
		return err
	}
	// Cleanup changes an exact Retain PV to Delete before deleting its PVC. A
	// retry must accept that already-applied state while validating every other
	// part of the PV/PVC identity.
	pvForValidation := pv
	if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
		pvForValidation = pv.DeepCopy()
		pvForValidation.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	}
	if err := validateRegularModelCachePVForPVC(
		binding, pvc, pvForValidation, expectedModes); err != nil {
		return err
	}
	if reader {
		if err := validateRegularModelCacheReaderPVMountOptions(binding, pvForValidation); err != nil {
			return err
		}
	}
	if len(binding.Spec.Resources.PersistentVolumeNames) != 0 &&
		!slices.Contains(binding.Spec.Resources.PersistentVolumeNames, pv.Name) {
		return fmt.Errorf("regular model cache PV %q does not match binding intent %v",
			pv.Name, binding.Spec.Resources.PersistentVolumeNames)
	}
	if binding.Status.Realized != nil && binding.Status.Realized.BoundPersistentVolumeName != "" &&
		binding.Status.Realized.BoundPersistentVolumeName != pv.Name {
		return fmt.Errorf("regular model cache PV %q does not match binding realized PV %q",
			pv.Name, binding.Status.Realized.BoundPersistentVolumeName)
	}
	return nil
}

func (c K8sComputeBackend) validateRegularModelCachePVClaimAfterWriterDelete(
	ctx context.Context,
	pv *corev1.PersistentVolume,
	writerPVC *corev1.PersistentVolumeClaim,
	binding *nvcav2beta1.ModelCacheBinding,
	namespace string,
	rwPVCName string,
	roPVCName string,
) error {
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != namespace {
		return fmt.Errorf("regular model cache PV %q has no binding-owned claimRef", pv.Name)
	}
	claimRef := pv.Spec.ClaimRef
	switch claimRef.Name {
	case rwPVCName:
		if err := validateRegularModelCachePVForPVC(binding, writerPVC, pv,
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}); err != nil {
			return fmt.Errorf("validate regular model cache writer PV: %w", err)
		}
	case roPVCName:
		readerPVC, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(namespace).
			Get(ctx, roPVCName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get existing regular model cache reader PVC %s/%s: %w",
				namespace, roPVCName, err)
		}
		if err := requireRegularModelCacheBindingUID(readerPVC, binding.UID); err != nil {
			return err
		}
		if !slices.Equal(readerPVC.Spec.AccessModes, ROAccessMode) {
			return fmt.Errorf("regular model cache reader PVC %s/%s does not use ReadOnlyMany",
				readerPVC.Namespace, readerPVC.Name)
		}
		if err := validateRegularModelCachePVForPVC(binding, readerPVC, pv, ROAccessMode); err != nil {
			return fmt.Errorf("validate regular model cache reader PV: %w", err)
		}
	default:
		return fmt.Errorf("regular model cache PV %q claimRef name %q is outside binding intent",
			pv.Name, claimRef.Name)
	}
	return nil
}

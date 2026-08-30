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
	"crypto/sha256"
	"fmt"
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

const modelCacheBindingNamePrefix = "model-cache-"

// NewModelCacheBinding builds the immutable binding intent for a durable
// request. Current NVMesh resource names are cache-handle scoped, so the
// binding name is also handle-scoped. A second workflow or sharing domain with
// the same handle therefore collides with, and must not adopt, the first
// binding instead of addressing the same resources through a different spec.
func NewModelCacheBinding(
	selection *PersistedModelCacheStorageSelection,
	sharingDomain string,
	cacheHandle string,
	writerNamespace string,
) (*nvcav2beta1.ModelCacheBinding, error) {
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if selection.Mode != ModelCacheSelectionDurable {
		return nil, fmt.Errorf("model cache binding requires a durable selection")
	}
	if strings.TrimSpace(sharingDomain) == "" {
		return nil, fmt.Errorf("model cache binding sharing domain is empty")
	}
	if strings.TrimSpace(cacheHandle) == "" {
		return nil, fmt.Errorf("model cache binding cache handle is empty")
	}
	if errs := validation.IsDNS1123Label(writerNamespace); len(errs) != 0 {
		return nil, fmt.Errorf("model cache binding writer namespace %q is invalid: %s",
			writerNamespace, strings.Join(errs, "; "))
	}

	rwPVCName := "rw-pvc-" + cacheHandle
	jobName := "writer-job-" + cacheHandle
	for kind, name := range map[string]string{
		"writer PVC": rwPVCName,
		"writer Job": jobName,
	} {
		if errs := validation.IsDNS1123Subdomain(name); len(errs) != 0 {
			return nil, fmt.Errorf("model cache binding %s name %q is invalid: %s",
				kind, name, strings.Join(errs, "; "))
		}
	}

	resources := nvcav2beta1.ModelCacheBindingResourceIntent{
		WriterNamespace:            writerNamespace,
		PersistentVolumeClaimNames: []string{rwPVCName},
		JobNames:                   []string{jobName},
	}
	if selection.Workflow == ModelCacheWorkflowRegular && selection.Transition == ModelCacheTransitionNVMesh {
		resources.PersistentVolumeClaimNames = append(
			resources.PersistentVolumeClaimNames, "ro-pvc-"+cacheHandle)
	} else if selection.Workflow == ModelCacheWorkflowHelm {
		resources.LeaseName = buildInitLeaseName(cacheHandle)
	}
	if selection.EncryptionRequired {
		switch selection.Workflow {
		case ModelCacheWorkflowRegular:
			// Keep this snapshot aligned with pkg/nvca/encryption, which
			// predates the Helm controller's resource naming convention.
			domainHash := hashNCAID(sharingDomain)
			resources.StorageClassNames = []string{domainHash + "-sc"}
			resources.SecretNames = []string{domainHash}
		case ModelCacheWorkflowHelm:
			resources.StorageClassNames = []string{buildStorageClassName(sharingDomain)}
			resources.SecretNames = []string{buildStorageClassSecretName(sharingDomain)}
		}
	}

	binding := &nvcav2beta1.ModelCacheBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: nvcav2beta1.SchemeGroupVersion.String(),
			Kind:       "ModelCacheBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       ModelCacheBindingName(cacheHandle),
			Namespace:  ModelCacheInitNamespace,
			Finalizers: []string{nvcav2beta1.ModelCacheBindingFinalizer},
		},
		Spec: nvcav2beta1.ModelCacheBindingSpec{
			Identity: nvcav2beta1.ModelCacheBindingIdentity{
				Version:             ModelCacheStorageSelectionVersion,
				Workflow:            nvcav2beta1.ModelCacheWorkflow(selection.Workflow),
				SharingDomainDigest: digestBindingValue(sharingDomain),
				CacheHandleDigest:   digestBindingValue(cacheHandle),
			},
			Decision: nvcav2beta1.ModelCacheBindingDecision{
				Provider:            selection.Provider,
				Provisioner:         selection.Provisioner,
				Transition:          selection.Transition,
				RequiredAccessModes: append([]corev1.PersistentVolumeAccessMode(nil), selection.RequiredAccessModes...),
				CatalogDigest:       selection.CatalogDigest,
				EncryptionRequired:  selection.EncryptionRequired,
			},
			StorageClass: nvcav2beta1.ModelCacheStorageClassSnapshot{
				Name:                selection.StorageClassName,
				UID:                 selection.StorageClassUID,
				ReclaimPolicy:       corev1.PersistentVolumeReclaimRetain,
				ConfigurationDigest: selection.StorageClassDigest,
			},
			Resources: resources,
		},
	}
	return binding, nil
}

// ModelCacheBindingName returns the deterministic, non-sensitive name used by
// the current handle-scoped NVMesh resource layout.
func ModelCacheBindingName(cacheHandle string) string {
	sum := sha256.Sum256([]byte(cacheHandle))
	return fmt.Sprintf("%s%x", modelCacheBindingNamePrefix, sum[:24])
}

func digestBindingValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum)
}

// ValidateModelCacheBinding proves that an existing binding is the exact
// immutable object this request intended to create.
func ValidateModelCacheBinding(
	binding *nvcav2beta1.ModelCacheBinding,
	selection *PersistedModelCacheStorageSelection,
	sharingDomain string,
	cacheHandle string,
	writerNamespace string,
) error {
	if err := ValidateModelCacheBindingIntent(
		binding, selection, sharingDomain, cacheHandle, writerNamespace); err != nil {
		return err
	}
	if binding.Status.Phase != nvcav2beta1.ModelCacheBindingPhaseActive {
		return fmt.Errorf("model cache binding %s/%s is not Active: %q",
			binding.Namespace, binding.Name, binding.Status.Phase)
	}
	return nil
}

// ValidateModelCacheBindingIntent validates identity and immutable spec without
// requiring status to be initialized. It is used only while recovering the
// create-before-status-update window.
func ValidateModelCacheBindingIntent(
	binding *nvcav2beta1.ModelCacheBinding,
	selection *PersistedModelCacheStorageSelection,
	sharingDomain string,
	cacheHandle string,
	writerNamespace string,
) error {
	if binding == nil {
		return fmt.Errorf("model cache binding is nil")
	}
	expected, err := NewModelCacheBinding(selection, sharingDomain, cacheHandle, writerNamespace)
	if err != nil {
		return err
	}
	if binding.Name != expected.Name || binding.Namespace != expected.Namespace {
		return fmt.Errorf("model cache binding identity %s/%s does not match expected %s/%s",
			binding.Namespace, binding.Name, expected.Namespace, expected.Name)
	}
	if !binding.DeletionTimestamp.IsZero() {
		return fmt.Errorf("model cache binding %s/%s is being deleted", binding.Namespace, binding.Name)
	}
	if !slices.Contains(binding.Finalizers, nvcav2beta1.ModelCacheBindingFinalizer) {
		return fmt.Errorf("model cache binding %s/%s has no protection finalizer", binding.Namespace, binding.Name)
	}
	if !reflect.DeepEqual(binding.Spec, expected.Spec) {
		return fmt.Errorf("model cache binding %s/%s immutable spec does not match the request intent",
			binding.Namespace, binding.Name)
	}
	if selection.BindingName != "" {
		if binding.Name != selection.BindingName || binding.UID != selection.BindingUID {
			return fmt.Errorf("model cache binding reference changed from %s/%s to %s/%s",
				selection.BindingName, selection.BindingUID, binding.Name, binding.UID)
		}
	}
	return nil
}

// ModelCacheBindingHasRequestReference reports whether status contains the
// exact request object, including its API-assigned UID.
func ModelCacheBindingHasRequestReference(
	binding *nvcav2beta1.ModelCacheBinding,
	namespace string,
	name string,
	uid types.UID,
) bool {
	for _, ref := range binding.Status.RequestReferences {
		if ref.Namespace == namespace && ref.Name == name && ref.UID == uid {
			return true
		}
	}
	return false
}

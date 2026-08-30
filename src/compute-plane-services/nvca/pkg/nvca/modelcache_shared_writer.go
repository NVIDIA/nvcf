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
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// prepareRWXReadOnlySharedWriterJob removes request metadata and rejects
// request-scoped inputs that a retained, binding-owned Job cannot safely keep.
// A future credential indirection must record every Secret in the binding and
// implement exact create, adoption, rotation, and cleanup before relaxing
// these restrictions.
func prepareRWXReadOnlySharedWriterJob(job *batchv1.Job) error {
	if job == nil {
		return fmt.Errorf("rwxReadOnly writer Job is nil")
	}

	// Only binding metadata is allowed to survive on the shared Job. The caller
	// adds the binding UID label after this function returns.
	job.OwnerReferences = nil
	job.Labels = nil
	job.Annotations = nil
	job.Spec.Template.OwnerReferences = nil
	job.Spec.Template.Labels = nil
	job.Spec.Template.Annotations = nil

	podSpec := &job.Spec.Template.Spec
	if len(podSpec.ImagePullSecrets) != 0 {
		return fmt.Errorf(
			"rwxReadOnly writer Job %s/%s references imagePullSecrets; "+
				"binding-scoped pull Secret identity and cleanup are not implemented",
			job.Namespace, job.Name)
	}

	for i := range podSpec.InitContainers {
		if err := validateRWXReadOnlySharedWriterContainer(
			job, "init container", &podSpec.InitContainers[i]); err != nil {
			return err
		}
	}
	for i := range podSpec.Containers {
		if err := validateRWXReadOnlySharedWriterContainer(
			job, "container", &podSpec.Containers[i]); err != nil {
			return err
		}
	}
	for i := range podSpec.Volumes {
		if rwxReadOnlyWriterVolumeReferencesSecret(&podSpec.Volumes[i]) {
			return fmt.Errorf(
				"rwxReadOnly writer Job %s/%s volume %q references a Secret; "+
					"binding-scoped Secret identity and cleanup are not implemented",
				job.Namespace, job.Name, podSpec.Volumes[i].Name)
		}
	}

	// The cache writer does not use the Kubernetes API. Avoid giving a retained
	// shared Job an implicit, namespace-scoped service-account credential.
	automountServiceAccountToken := false
	podSpec.AutomountServiceAccountToken = &automountServiceAccountToken
	return nil
}

func validateRWXReadOnlySharedWriterContainer(
	job *batchv1.Job,
	kind string,
	container *corev1.Container,
) error {
	if len(container.Env) != 0 || len(container.EnvFrom) != 0 {
		return fmt.Errorf(
			"rwxReadOnly writer Job %s/%s %s %q retains environment input; "+
				"binding-scoped credential indirection and immutable input identity are not implemented",
			job.Namespace, job.Name, kind, container.Name)
	}
	return nil
}

func rwxReadOnlyWriterVolumeReferencesSecret(volume *corev1.Volume) bool {
	if volume == nil {
		return false
	}
	if volume.Secret != nil ||
		(volume.CSI != nil && volume.CSI.NodePublishSecretRef != nil) ||
		(volume.FlexVolume != nil && volume.FlexVolume.SecretRef != nil) ||
		(volume.CephFS != nil && volume.CephFS.SecretRef != nil) ||
		(volume.Cinder != nil && volume.Cinder.SecretRef != nil) ||
		(volume.RBD != nil && volume.RBD.SecretRef != nil) ||
		(volume.ScaleIO != nil && volume.ScaleIO.SecretRef != nil) ||
		(volume.StorageOS != nil && volume.StorageOS.SecretRef != nil) ||
		(volume.AzureFile != nil && volume.AzureFile.SecretName != "") {
		return true
	}
	if volume.Projected == nil {
		return false
	}
	for _, source := range volume.Projected.Sources {
		if source.Secret != nil {
			return true
		}
	}
	return false
}

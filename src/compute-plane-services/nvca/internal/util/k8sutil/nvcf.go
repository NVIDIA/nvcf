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

package k8sutil

import (
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Set to true when the image cred updater init job is completed
	// so job can be cleaned up.
	ImageCredUpdaterInitJobCompletedAnnotationKey = "nvca.nvcf.nvidia.io/image-cred-init-completed" //nolint:gosec

	// TaskCleanupExtraGracePeriod is extra time allotted to tasks before forced cleanup by NVCA.
	TaskCleanupExtraGracePeriod = 5 * time.Minute
)

// FindNVCFImagePullSecretObjects filters objs for NVCF worker and workload pull secrets.
// It returns an error if either are not found.
func FindNVCFImagePullSecretObjects(objs ...metav1.Object) (workerImagePullSecrets, workloadImagePullSecrets []*corev1.Secret, err error) {
	for _, obj := range objs {
		t, ok := obj.(*corev1.Secret)
		if !ok {
			continue
		}
		if t.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		if IsNVCFWorkerImagePullSecretObject(t) {
			workerImagePullSecrets = append(workerImagePullSecrets, t)
		} else if IsNVCFWorkloadImagePullSecretObject(t) {
			workloadImagePullSecrets = append(workloadImagePullSecrets, t)
		}
	}
	if len(workerImagePullSecrets) == 0 {
		return nil, nil, fmt.Errorf("no worker image pull secret found")
	}
	if len(workloadImagePullSecrets) == 0 {
		return nil, nil, fmt.Errorf("no workload image pull secret found")
	}
	return workerImagePullSecrets, workloadImagePullSecrets, nil
}

// FindNVCFWorkerImagePullSecretObjects filters objs for NVCF worker pull secrets only.
// Use this for container function paths where workload pull secrets are not required
// (e.g., ECR images authenticated via node IAM role — no explicit pull secret is provided).
func FindNVCFWorkerImagePullSecretObjects(objs ...metav1.Object) ([]*corev1.Secret, error) {
	var workerImagePullSecrets []*corev1.Secret
	for _, obj := range objs {
		t, ok := obj.(*corev1.Secret)
		if !ok {
			continue
		}
		if t.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		if IsNVCFWorkerImagePullSecretObject(t) {
			workerImagePullSecrets = append(workerImagePullSecrets, t)
		}
	}
	if len(workerImagePullSecrets) == 0 {
		return nil, fmt.Errorf("no worker image pull secret found")
	}
	return workerImagePullSecrets, nil
}

func IsNVCFWorkerImagePullSecretObject(secret *corev1.Secret) bool {
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return false
	}
	if strings.HasSuffix(secret.Name, "-core") || strings.HasSuffix(secret.Name, "-worker") ||
		strings.HasPrefix(secret.Name, "worker-") {
		return true
	}
	return false
}

func IsNVCFWorkloadImagePullSecretObject(secret *corev1.Secret) bool {
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return false
	}
	if strings.HasSuffix(secret.Name, "-inference") || strings.HasSuffix(secret.Name, "-task") ||
		strings.HasPrefix(secret.Name, "workload-") || secret.Name == common.HelmWorkloadPullSecretName {
		return true
	}
	return false
}

// HasTaskPodExceededTimeout determines if pod has exceeded either maxQueuedDuration or maxRuntimeDuration
// depending on what state pod.Status indicates.
func HasTaskPodExceededTimeout(pod *corev1.Pod, maxQueuedDuration, maxRuntimeDuration time.Duration, now time.Time) bool {
	ps := pod.Status
	if ps.StartTime != nil && !ps.StartTime.IsZero() {
		// Start time is set after scheduling, so runtime countdown has started.
		return IsOverTimeout(*ps.StartTime, maxRuntimeDuration, now)
	}

	// If the pod hasn't started, it is still considered enqueued so rely on creation timestamp with queue timeout.
	return !pod.CreationTimestamp.IsZero() && IsOverTimeout(pod.CreationTimestamp, maxQueuedDuration, now)
}

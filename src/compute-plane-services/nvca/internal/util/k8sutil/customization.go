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
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// NVCA System Namespace
	NVCASystemNamespace = "nvca-system"
	// CustomAnnotationsConfigMapName is the name of the ConfigMap that stores custom annotations
	CustomAnnotationsConfigMapName = "nvca-namespace-pod-annotations" // #nosec G101
	// CustomSecretsConfigMapName is the name of the ConfigMap that stores custom secrets
	CustomSecretsConfigMapName = "nvca-namespace-secrets" // #nosec G101
	// CustomSecretPrefix is the prefix used to identify custom secrets in the configmap
	CustomSecretPrefix = "nvcf-custom-" // #nosec G101
	// CustomAnnotationsMapKey is the key value in the configmap for the annotations
	CustomAnnotationsMapKey = "annotations" // #nosec G101
)

// GetCustomSecrets retrieves custom secrets from the ConfigMap
func GetCustomSecrets(ctx context.Context, k8sClient kubernetes.Interface, namespace string) ([]corev1.Secret, error) {
	cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, CustomSecretsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []corev1.Secret{}, nil
		}
		return nil, fmt.Errorf("failed to get custom secrets configmap: %w", err)
	}

	var secrets []corev1.Secret
	for key, data := range cm.Data {
		if !strings.HasPrefix(key, CustomSecretPrefix) {
			continue
		}
		var secret corev1.Secret
		if err := json.Unmarshal([]byte(data), &secret); err != nil {
			return nil, fmt.Errorf("failed to unmarshal secret from key %s: %w", key, err)
		}
		secrets = append(secrets, secret)
	}

	return secrets, nil
}

// ApplyCustomAnnotations applies cached custom annotations to a pod from a sync.Map.
// This function is used by both the backend and reconciler to apply custom annotations
// stored in BackendK8sCache to pods.
func ApplyCustomAnnotations(pod *corev1.Pod, customAnnotations *sync.Map) {
	if customAnnotations == nil {
		return
	}

	// Get cached custom annotations
	cachedAnnotations, ok := customAnnotations.Load("annotations")
	if !ok || cachedAnnotations == nil {
		return
	}

	annotations, ok := cachedAnnotations.(map[string]string)
	if !ok || len(annotations) == 0 {
		return
	}

	// Apply custom annotations
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	maps.Copy(pod.Annotations, annotations)
}

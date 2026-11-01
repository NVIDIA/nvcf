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
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
)

// UpdateServiceAccountImagePullSecrets updates the service account saName in namespace
// with any secret names in imagePullSecrets not already present.
func UpdateServiceAccountImagePullSecrets(
	ctx context.Context,
	kubeClients *kubeclients.KubeClients,
	crClient client.Client,
	namespace, saName string,
	imagePullSecrets []*corev1.Secret,
) (updated bool, err error) {
	var sa *corev1.ServiceAccount
	if kubeClients != nil {
		sa, err = kubeClients.K8s.CoreV1().ServiceAccounts(namespace).Get(ctx, saName, metav1.GetOptions{})
	} else {
		sa = &corev1.ServiceAccount{}
		err = crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: saName}, sa)
	}
	if err != nil {
		return false, fmt.Errorf("get instance service account: %w", err)
	}

	existingImagePullSecretNames := sets.New[string]()
	for _, secret := range sa.ImagePullSecrets {
		existingImagePullSecretNames.Insert(secret.Name)
	}

	needsUpdate := false
	for _, imagePullSecret := range imagePullSecrets {
		if !existingImagePullSecretNames.Has(imagePullSecret.Name) {
			sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: imagePullSecret.Name})
			needsUpdate = true
		}
	}

	if !needsUpdate {
		return false, nil
	}

	sort.Slice(sa.ImagePullSecrets, func(i, j int) bool {
		return sa.ImagePullSecrets[i].Name < sa.ImagePullSecrets[j].Name
	})

	if kubeClients != nil {
		_, err = kubeClients.K8s.CoreV1().ServiceAccounts(namespace).Update(ctx, sa, metav1.UpdateOptions{})
	} else {
		err = crClient.Update(ctx, sa)
	}

	if err != nil {
		return false, fmt.Errorf("update service account with image pull secrets: %w", err)
	}

	return true, nil
}

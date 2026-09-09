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

package selfhosted

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckNamedControlPlaneInstallAllowed_DefaultOwnerIsNoop(t *testing.T) {
	require.NoError(t, CheckNamedControlPlaneInstallAllowed(context.Background(), nil, ""))
	require.NoError(t, CheckNamedControlPlaneInstallAllowed(context.Background(), nil, mixedModeDefaultOwner))
}

func TestCheckNamedControlPlaneInstallAllowed_BlocksUnadoptedLegacyObjects(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "nvcf", Name: "nvcf-api"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Namespace: "nats-system",
			Name:      "nats",
			Labels:    map[string]string{mixedModeOwnerLabel: mixedModeDefaultOwner},
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "nvca"}},
		&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{
			Name:   "nvca",
			Labels: map[string]string{mixedModeOwnerLabel: "plane-b"},
		}},
	)

	err := CheckNamedControlPlaneInstallAllowed(context.Background(), client, "plane-a")

	var unadopted *UnadoptedLegacyObjectsError
	require.ErrorAs(t, err, &unadopted)
	assert.Equal(t, "plane-a", unadopted.ControlPlaneOwner)
	assert.Equal(t, []string{
		"clusterrole/nvca",
		"deployment/nvcf/nvcf-api",
		"namespace/nvcf",
	}, unadopted.Objects)
	assert.Contains(t, err.Error(), "cannot install named control plane \"plane-a\"")
	assert.Contains(t, err.Error(), "upgrade the legacy control plane first")
	assert.NotContains(t, err.Error(), "statefulset/nats-system/nats")
	assert.NotContains(t, err.Error(), "mutatingwebhookconfiguration/nvca")
}

func TestFindUnadoptedLegacyObjects_IgnoresAdoptedAndMissingObjects(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "nvcf",
			Labels: map[string]string{mixedModeOwnerLabel: mixedModeDefaultOwner},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "cert-manager",
			Labels: map[string]string{mixedModeOwnerLabel: "shared"},
		}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Namespace: "nvcf",
			Name:      "nvcf-api",
			Labels:    map[string]string{mixedModeOwnerLabel: "plane-b"},
		}},
	)

	objects, err := FindUnadoptedLegacyObjects(context.Background(), client)

	require.NoError(t, err)
	assert.Empty(t, objects)
}

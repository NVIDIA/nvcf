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

package encryption

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
)

// MockKubeClients is a mock implementation of KubeClients
type MockKubeClients struct {
	mock.Mock
	K8s *fake.Clientset
}

func TestBuildMD5Hash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "d41d8cd98f00b204e9800998ecf8427e",
		},
		{
			name:     "simple string",
			input:    "test",
			expected: "098f6bcd4621d373cade4e832627b4f6",
		},
		{
			name:     "complex string",
			input:    "test@123!",
			expected: "b062ad5bb18ac889ae042465a01377fc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildMD5Hash(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildStorageClassName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple hash",
			input:    "abc123",
			expected: "abc123-sc",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "-sc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildStorageClassName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateToken(t *testing.T) {
	ctx := context.Background()

	// Test that generated tokens are different
	token1 := generateToken(ctx, "test1")
	token2 := generateToken(ctx, "test2")
	assert.NotEqual(t, token1, token2)

	// Test that token is base64 encoded
	assert.NotEmpty(t, token1)

	// Test fallback to base64 encoded name
	token3 := generateToken(ctx, "test3")
	assert.NotEmpty(t, token3)
}

func TestSetupEncryption(t *testing.T) {
	ctx := context.Background()
	clients := &kubeclients.KubeClients{
		K8s: fake.NewSimpleClientset(),
	}

	ncaId := "test-nca"
	namespace := "test-namespace"

	scName, err := SetupEncryption(ctx, clients, ncaId, namespace)
	assert.NoError(t, err)
	assert.NotEmpty(t, scName)

	// Verify storage class was created
	sc, err := clients.K8s.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, StorageClassProvisioner, sc.Provisioner)
	assert.Equal(t, StorageClassBindMode, string(*sc.VolumeBindingMode))

	// Verify secret was created
	secretName := BuildMD5Hash(ncaId)
	secret, err := clients.K8s.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotEmpty(t, secret.Data["dmcryptKey"])
}

func TestEnsureNVMeshEncryptionStorageClass(t *testing.T) {
	ctx := context.Background()
	clients := &kubeclients.KubeClients{
		K8s: fake.NewSimpleClientset(),
	}

	secretName := "test-secret"
	namespace := "test-namespace"
	scName := "test-sc"

	err := ensureNVMeshEncryptionStorageClass(ctx, clients, secretName, namespace, scName)
	assert.NoError(t, err)

	// Verify storage class was created with correct parameters
	sc, err := clients.K8s.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, StorageClassProvisioner, sc.Provisioner)
	assert.Equal(t, StorageClassVPGType, sc.Parameters[StorageClassVPG])
	assert.Equal(t, StorageClassFS, sc.Parameters[StorageClassCSIFS])
	assert.Equal(t, secretName, sc.Parameters[StorageClassCSISecret])
	assert.Equal(t, namespace, sc.Parameters[StorageClassCSINS])
}

func TestEnsureNVMeshEncryptionStorageClassValidatesExisting(t *testing.T) {
	const (
		secretName = "test-secret"
		namespace  = "test-namespace"
		scName     = "test-sc"
	)

	tests := []struct {
		name      string
		mutate    func(*storagev1.StorageClass)
		wantField string
	}{
		{
			name: "provisioner",
			mutate: func(sc *storagev1.StorageClass) {
				sc.Provisioner = "unexpected.example.com"
			},
			wantField: "provisioner",
		},
		{
			name: "reclaim policy",
			mutate: func(sc *storagev1.StorageClass) {
				policy := corev1.PersistentVolumeReclaimDelete
				sc.ReclaimPolicy = &policy
			},
			wantField: "reclaimPolicy",
		},
		{
			name: "binding mode",
			mutate: func(sc *storagev1.StorageClass) {
				mode := storagev1.VolumeBindingWaitForFirstConsumer
				sc.VolumeBindingMode = &mode
			},
			wantField: "volumeBindingMode",
		},
		{
			name: "volume expansion",
			mutate: func(sc *storagev1.StorageClass) {
				allowed := false
				sc.AllowVolumeExpansion = &allowed
			},
			wantField: "allowVolumeExpansion",
		},
		{
			name: "parameters",
			mutate: func(sc *storagev1.StorageClass) {
				sc.Parameters["unexpected"] = "value"
			},
			wantField: "parameters",
		},
		{
			name: "mount options",
			mutate: func(sc *storagev1.StorageClass) {
				sc.MountOptions = []string{"unexpected"}
			},
			wantField: "mountOptions",
		},
		{
			name: "allowed topologies",
			mutate: func(sc *storagev1.StorageClass) {
				sc.AllowedTopologies = []corev1.TopologySelectorTerm{{}}
			},
			wantField: "allowedTopologies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := expectedNVMeshEncryptionStorageClass(secretName, namespace, scName)
			tt.mutate(sc)
			clients := &kubeclients.KubeClients{K8s: fake.NewSimpleClientset(sc)}

			err := ensureNVMeshEncryptionStorageClass(
				context.Background(), clients, secretName, namespace, scName)

			assert.EqualError(t, err,
				"existing NVMesh encryption StorageClass \"test-sc\" has unexpected "+tt.wantField)
		})
	}
}

func TestSetupEncryptionPreservesExistingSecret(t *testing.T) {
	const (
		ncaID     = "test-nca"
		namespace = "test-namespace"
	)
	secretName := BuildMD5Hash(ncaID)
	scName := BuildStorageClassName(secretName)
	wantData := map[string][]byte{
		"dmcryptKey": []byte("existing-key"),
		"extra":      []byte("preserve-me"),
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       wantData,
	}
	clients := &kubeclients.KubeClients{K8s: fake.NewSimpleClientset(
		secret, expectedNVMeshEncryptionStorageClass(secretName, namespace, scName),
	)}

	gotSCName, err := SetupEncryption(context.Background(), clients, ncaID, namespace)

	assert.NoError(t, err)
	assert.Equal(t, scName, gotSCName)
	actual, err := clients.K8s.CoreV1().Secrets(namespace).Get(
		context.Background(), secretName, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, wantData, actual.Data)
}

func TestEnsureNVMeshEncryptionSecretRejectsMissingKey(t *testing.T) {
	const (
		name      = "test-secret"
		namespace = "test-namespace"
	)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{"unrelated": []byte("value")},
	}
	clients := &kubeclients.KubeClients{K8s: fake.NewSimpleClientset(secret)}

	err := ensureNVMeshEncryptionSecret(context.Background(), clients, name, namespace)

	assert.EqualError(t, err,
		"existing NVMesh encryption Secret test-namespace/test-secret has no dmcryptKey")
}

func TestEnsureNVMeshEncryptionSecret(t *testing.T) {
	ctx := context.Background()
	clients := &kubeclients.KubeClients{
		K8s: fake.NewSimpleClientset(),
	}

	name := "test-secret"
	namespace := "test-namespace"

	err := ensureNVMeshEncryptionSecret(ctx, clients, name, namespace)
	assert.NoError(t, err)

	// Verify secret was created
	secret, err := clients.K8s.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotEmpty(t, secret.Data["dmcryptKey"])
}

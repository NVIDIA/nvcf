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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/encryption"
)

const ncaId = "D_zpC1AKku19vaqWc9UUDQQdee0ljIfFCWuxf7TCukE"
const namespace = "nvcf-backend"

func setUp(t *testing.T, ctx context.Context, clients *kubeclients.KubeClients) (string, string) {
	ncaHash := encryption.BuildMD5Hash(ncaId)
	expectedSCName := encryption.BuildStorageClassName(ncaHash)

	scName, err := encryption.SetupEncryption(ctx, clients, ncaId, namespace)
	assert.Nil(t, err)
	assert.Equal(t, scName, expectedSCName)

	return ncaHash, expectedSCName
}

func validateSetup(t *testing.T, ctx context.Context, clients *kubeclients.KubeClients, ncaHash, expectedSCName string) {
	//Get secret
	secret, err := clients.K8s.CoreV1().Secrets(namespace).Get(ctx, ncaHash, metav1.GetOptions{})
	assert.Nil(t, err)
	_, exists := secret.Data["dmcryptKey"]
	assert.Equal(t, true, exists)

	//Get StorageClass
	sc, err := clients.K8s.StorageV1().StorageClasses().Get(ctx, expectedSCName, metav1.GetOptions{})
	assert.Nil(t, err)

	paramMap := map[string]string{
		encryption.StorageClassVPG:       encryption.StorageClassVPGType,
		encryption.StorageClassCSIFS:     encryption.StorageClassFS,
		encryption.StorageClassCSISecret: ncaHash,
		encryption.StorageClassCSINS:     namespace,
	}

	for k, v := range paramMap {
		vsc, exists := sc.Parameters[k]
		assert.Equal(t, true, exists)
		assert.Equal(t, v, vsc)
	}
}

func TestNVMeshEncryptionSetup(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)
	_ = core.SetLevel(log, "debug")

	clients := mockKubeClients()

	ncaHash, expectedSCName := setUp(t, ctx, clients)
	validateSetup(t, ctx, clients, ncaHash, expectedSCName)

}

func TestNVMeshEncryptionSetupNoSecret(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)
	_ = core.SetLevel(log, "debug")

	clients := mockKubeClients()
	ncaHash, expectedSCName := setUp(t, ctx, clients)

	//Delete secret
	err := clients.K8s.CoreV1().Secrets(namespace).Delete(ctx, ncaHash, metav1.DeleteOptions{})
	require.NoError(t, err)

	//Get secret
	_, err = clients.K8s.CoreV1().Secrets(namespace).Get(ctx, encryption.BuildMD5Hash(ncaId), metav1.GetOptions{})
	assert.Equal(t, true, errors.IsNotFound(err))

	_, _ = setUp(t, ctx, clients)
	validateSetup(t, ctx, clients, ncaHash, expectedSCName)
}

func TestNVMeshEncryptionSetupNoStorageClass(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)
	_ = core.SetLevel(log, "debug")

	clients := mockKubeClients()
	ncaHash, expectedSCName := setUp(t, ctx, clients)

	//Delete StorageClass
	err := clients.K8s.StorageV1().StorageClasses().Delete(ctx, expectedSCName, metav1.DeleteOptions{})
	require.NoError(t, err)

	//Get StorageClass
	_, err = clients.K8s.StorageV1().StorageClasses().Get(ctx, expectedSCName, metav1.GetOptions{})
	assert.Equal(t, true, errors.IsNotFound(err))

	_, _ = setUp(t, ctx, clients)
	validateSetup(t, ctx, clients, ncaHash, expectedSCName)
}

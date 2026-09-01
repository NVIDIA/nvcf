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
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

const KEY_BYTES = 32

const (
	StorageClassBindMode    = "Immediate"
	StorageClassProvisioner = "nvmesh-csi.excelero.com"
	StorageClassVPG         = "vpg"
	StorageClassVPGType     = "DEFAULT_RAID_10_VPG"
	StorageClassFS          = "xfs"
	StorageClassCSIFS       = "csi.storage.k8s.io/fstype"
	StorageClassCSISecret   = "csi.storage.k8s.io/node-stage-secret-name"
	StorageClassCSINS       = "csi.storage.k8s.io/node-stage-secret-namespace"
)

func SetupEncryption(ctx context.Context, clients *kubeclients.KubeClients, ncaId, namespace string) (string, error) {
	logger := core.GetLogger(ctx)
	ncaHash := BuildMD5Hash(ncaId)
	scName := BuildStorageClassName(ncaHash)
	logger.WithFields(logrus.Fields{
		"nca_id":   ncaId,
		"nca_hash": ncaHash,
	}).Infof("Setting up NVMesh encryption")

	err := ensureNVMeshEncryptionSecret(ctx, clients, ncaHash, namespace)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"nca_id": ncaId,
			"error":  err.Error(),
		}).Errorf("Unable to set up NVMesh encryption Secret")
		return "", err
	}

	err = ensureNVMeshEncryptionStorageClass(ctx, clients, ncaHash, namespace, scName)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"nca_id": ncaId,
			"error":  err.Error(),
		}).Errorf("Unable to set up NVMesh encryption StorageClass")
		return "", err
	}

	return scName, nil
}

func ensureNVMeshEncryptionStorageClass(ctx context.Context, clients *kubeclients.KubeClients, secretName, namespace, scName string) error {
	logger := core.GetLogger(ctx)
	existing, err := clients.K8s.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("storageclass", err)
	}

	if err == nil {
		return validateNVMeshEncryptionStorageClass(existing, expectedNVMeshEncryptionStorageClass(
			secretName, namespace, scName))
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get NVMesh encryption StorageClass %q: %w", scName, err)
	}

	logger.WithFields(logrus.Fields{
		"storageclass": scName,
		"namespace":    namespace,
	}).Debugf("Creating StorageClass")

	_, err = clients.K8s.StorageV1().StorageClasses().Create(ctx,
		expectedNVMeshEncryptionStorageClass(secretName, namespace, scName), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create NVMesh encryption StorageClass %q: %w", scName, err)
	}
	return nil
}

func expectedNVMeshEncryptionStorageClass(secretName, namespace, scName string) *storagev1.StorageClass {
	allowedExpansion := true
	reclaimPolicy := corev1.PersistentVolumeReclaimRetain
	bindingMode := storagev1.VolumeBindingMode(StorageClassBindMode)

	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
		},
		Provisioner:          StorageClassProvisioner,
		AllowVolumeExpansion: &allowedExpansion,
		VolumeBindingMode:    &bindingMode,
		ReclaimPolicy:        &reclaimPolicy,
		Parameters: map[string]string{
			StorageClassVPG:       StorageClassVPGType,
			StorageClassCSIFS:     StorageClassFS,
			StorageClassCSISecret: secretName,
			StorageClassCSINS:     namespace,
		},
	}
}

func validateNVMeshEncryptionStorageClass(actual, expected *storagev1.StorageClass) error {
	invalid := func(field string) error {
		return fmt.Errorf("existing NVMesh encryption StorageClass %q has unexpected %s", actual.Name, field)
	}
	if actual.Provisioner != expected.Provisioner {
		return invalid("provisioner")
	}
	if actual.ReclaimPolicy == nil || expected.ReclaimPolicy == nil ||
		*actual.ReclaimPolicy != *expected.ReclaimPolicy {
		return invalid("reclaimPolicy")
	}
	if actual.VolumeBindingMode == nil || expected.VolumeBindingMode == nil ||
		*actual.VolumeBindingMode != *expected.VolumeBindingMode {
		return invalid("volumeBindingMode")
	}
	if actual.AllowVolumeExpansion == nil || expected.AllowVolumeExpansion == nil ||
		*actual.AllowVolumeExpansion != *expected.AllowVolumeExpansion {
		return invalid("allowVolumeExpansion")
	}
	if !maps.Equal(actual.Parameters, expected.Parameters) {
		return invalid("parameters")
	}
	if len(actual.MountOptions) != 0 {
		return invalid("mountOptions")
	}
	if len(actual.AllowedTopologies) != 0 {
		return invalid("allowedTopologies")
	}
	return nil
}

func ensureNVMeshEncryptionSecret(ctx context.Context, clients *kubeclients.KubeClients, name, namespace string) error {
	logger := core.GetLogger(ctx)
	secret, err := clients.K8s.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("secret", err)
	}

	if err == nil {
		if len(secret.Data["dmcryptKey"]) == 0 {
			return fmt.Errorf("existing NVMesh encryption Secret %s/%s has no dmcryptKey", namespace, name)
		}
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get NVMesh encryption Secret %s/%s: %w", namespace, name, err)
	}

	logger.WithFields(logrus.Fields{
		"nca_hash":  name,
		"namespace": namespace,
	}).Debugf("Creating secret")

	secretRequest := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: map[string][]byte{
			"dmcryptKey": []byte(generateToken(ctx, name)),
		},
	}
	if _, err := clients.K8s.CoreV1().Secrets(namespace).Create(ctx, secretRequest, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create NVMesh encryption Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// Generate the Random KEY_BYTES byte token
func generateToken(ctx context.Context, name string) string {
	logger := core.GetLogger(ctx)
	token := make([]byte, KEY_BYTES)
	_, err := rand.Read(token)
	if err != nil {
		logger.Errorf("Found error while generating the secret token - %v", err)
		//should we fail? For now I am going to base64 encode name and use that
		return base64.StdEncoding.EncodeToString([]byte(name))
	}
	base64EncodedToken := base64.StdEncoding.EncodeToString(token)
	return base64EncodedToken
}

func BuildMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}

func BuildStorageClassName(text string) string {
	return text + "-sc"
}

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
	"context"
	"fmt"
	"io"

	//nolint:gosec
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	NVMeshKeyBytes = 32

	NVMeshStorageClassBindMode    = "Immediate"
	NVMeshStorageClassProvisioner = "nvmesh-csi.excelero.com"
	NVMeshStorageClassVPG         = "vpg"
	NVMeshStorageClassVPGType     = "DEFAULT_RAID_10_VPG"
	NVMeshStorageClassFS          = "xfs"
	NVMeshStorageClassCSIFS       = "csi.storage.k8s.io/fstype"
	//nolint:gosec
	NVMeshStorageClassCSISecret = "csi.storage.k8s.io/node-stage-secret-name"
	NVMeshStorageClassCSINS     = "csi.storage.k8s.io/node-stage-secret-namespace"

	// If this annotation is present on a storage class, it is an encrypted storage class
	// specifically used for model caches and can be reconciled by this controller.
	encryptedModelCacheStorageClassAnnotation      = fqdnPrefix + "/modelcache-encrypted-sc"
	encryptedModelCacheStorageClassAnnotationValue = "true"
)

func (r *Reconciler) doEncryptedStorageClassNVMesh(ctx context.Context,
	st *nvcav1new.StorageRequest,
	ncaID string,
) (string, error) {
	if ncaID == "" {
		return "", reconcile.TerminalError(fmt.Errorf("nca ID annotation value is empty on storage request"))
	}

	scName, err := r.createEncryptedStorageClassNVMesh(ctx, st, ncaID)
	if err != nil {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:   "Encrypted",
			Status: metav1.ConditionFalse,
			Reason: "EncryptionError",
		})
		return "", reconcile.TerminalError(err)
	}

	meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
		Type:   "Encrypted",
		Status: metav1.ConditionTrue,
		Reason: "EncryptionSucceeded",
	})
	return scName, nil
}

func (r *Reconciler) createEncryptedStorageClassNVMesh(ctx context.Context,
	st *nvcav1new.StorageRequest,
	ncaID string,
) (string, error) {
	log := logf.FromContext(ctx)

	log = log.WithValues(
		"nca_id", ncaID,
	)
	ctx = logf.IntoContext(ctx, log)

	secretName, err := r.ensureNVMeshEncryptionSecret(ctx, st, ncaID)
	if err != nil {
		log.Error(err, "Failed to set up NVMesh encryption Secret")
		return "", err
	}

	scName, err := r.ensureNVMeshEncryptionStorageClass(ctx, st, ncaID, secretName)
	if err != nil {
		log.Error(err, "Failed to set up NVMesh encryption StorageClass")
		return "", err
	}

	return scName, nil
}

func (r *Reconciler) ensureNVMeshEncryptionStorageClass(
	ctx context.Context,
	st *nvcav1new.StorageRequest,
	ncaID, secretName string,
) (string, error) {
	log := logf.FromContext(ctx)

	scName := buildStorageClassName(ncaID)

	allowedExpansion := true
	reclaimPolicy := corev1.PersistentVolumeReclaimRetain
	var bindingMode storagev1.VolumeBindingMode = NVMeshStorageClassBindMode

	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
			Labels: map[string]string{
				types.NCAIDKey: st.Labels[types.NCAIDKey],
			},
			Annotations: map[string]string{
				encryptedModelCacheStorageClassAnnotation: encryptedModelCacheStorageClassAnnotationValue,
			},
		},
		Provisioner:          NVMeshStorageClassProvisioner,
		AllowVolumeExpansion: &allowedExpansion,
		VolumeBindingMode:    &bindingMode,
		ReclaimPolicy:        &reclaimPolicy,
		Parameters: map[string]string{
			NVMeshStorageClassVPG:       NVMeshStorageClassVPGType,
			NVMeshStorageClassCSIFS:     NVMeshStorageClassFS,
			NVMeshStorageClassCSISecret: secretName,
			NVMeshStorageClassCSINS:     ModelCacheInitNamespace,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, sc, noMutF)
	if op == controllerutil.OperationResultCreated {
		log.Info("Created NVMesh encryption StorageClass", "storageclass", sc.Name)
	}
	return scName, err
}

func (r *Reconciler) ensureNVMeshEncryptionSecret(
	ctx context.Context,
	st *nvcav1new.StorageRequest,
	ncaID string,
) (string, error) {
	log := logf.FromContext(ctx)

	secretName := buildStorageClassSecretName(ncaID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ModelCacheInitNamespace,
			Labels: map[string]string{
				types.NCAIDKey: st.Labels[types.NCAIDKey],
			},
		},
		Data: map[string][]byte{
			"dmcryptKey": []byte(generateToken(ctx, r.randReader, secretName)),
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, noMutF)
	if op == controllerutil.OperationResultCreated {
		log.Info("Created NVMesh encryption Secret", "secret_name", secret.Name)
	}
	return secretName, err
}

// Generate the Random NVMeshKeyBytes byte token
func generateToken(ctx context.Context, randReader io.Reader, secretName string) string {
	token := make([]byte, NVMeshKeyBytes)
	if _, err := io.ReadFull(randReader, token); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to generate the secret token, using b64 encoded secret name")
		// should we fail? For now I am going to base64 encode name and use that
		return base64.StdEncoding.EncodeToString([]byte(secretName))
	}
	base64EncodedToken := base64.StdEncoding.EncodeToString(token)
	return base64EncodedToken
}

func buildStorageClassName(ncaID string) string {
	return "sc-" + hashNCAID(ncaID)
}

func buildStorageClassSecretName(ncaID string) string {
	return "scsec-" + hashNCAID(ncaID)
}

func hashNCAID(ncaID string) string {
	sum := md5.Sum([]byte(ncaID)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

func isStorageClassEncrypted(sc *storagev1.StorageClass) bool {
	return sc.Annotations != nil &&
		sc.Annotations[encryptedModelCacheStorageClassAnnotation] ==
			encryptedModelCacheStorageClassAnnotationValue
}

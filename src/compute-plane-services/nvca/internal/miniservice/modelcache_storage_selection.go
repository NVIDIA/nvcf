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

package mscontroller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func (r *Reconciler) selectHelmCacheBackend(
	ctx context.Context,
	icmsReq *nvcav2beta1.ICMSRequest,
	instanceNamespace string,
) (nvcastorage.HelmCacheBackend, error) {
	raw := icmsReq.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
	if raw != "" {
		selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(raw)
		if err != nil {
			return "", reconcile.TerminalError(err)
		}
		backend, err := nvcastorage.HelmCacheBackendFromSelection(selection)
		if err != nil {
			return "", reconcile.TerminalError(err)
		}
		if err := r.validateExistingModelCacheStorageRequest(
			ctx, icmsReq, instanceNamespace, raw, backend); err != nil {
			return "", err
		}
		return backend, nil
	}

	existing := &nvcav2beta1.StorageRequest{}
	key := client.ObjectKey{
		Namespace: instanceNamespace,
		Name:      nvcav2beta1.ModelCacheRequest.Name(),
	}
	switch err := r.Client.Get(ctx, key, existing); {
	case err == nil:
		if existing.Spec.ModelCache == nil {
			return "", fmt.Errorf("existing model cache StorageRequest %s/%s has no modelCache spec",
				key.Namespace, key.Name)
		}
		return nvcastorage.PersistedHelmCacheBackend(existing.Spec.ModelCache.Backend)
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("get existing model cache StorageRequest %s/%s: %w",
			key.Namespace, key.Name, err)
	}

	return nvcastorage.SelectHelmCacheBackend(
		ctx, r.Client, r.FeatureFlagFetcher, r.cfg.Agent.ModelCache.StorageClassName)
}

func (r *Reconciler) validateExistingModelCacheStorageRequest(
	ctx context.Context,
	icmsReq *nvcav2beta1.ICMSRequest,
	instanceNamespace string,
	selectionPayload string,
	backend nvcastorage.HelmCacheBackend,
) error {
	existing := &nvcav2beta1.StorageRequest{}
	key := client.ObjectKey{Namespace: instanceNamespace, Name: nvcav2beta1.ModelCacheRequest.Name()}
	if err := r.Client.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get existing model cache StorageRequest %s/%s: %w",
			key.Namespace, key.Name, err)
	}
	if err := validatePersistedModelCacheStorageRequest(
		existing, icmsReq, selectionPayload, backend); err != nil {
		return reconcile.TerminalError(err)
	}
	return nil
}

func validatePersistedModelCacheStorageRequest(
	existing *nvcav2beta1.StorageRequest,
	icmsReq *nvcav2beta1.ICMSRequest,
	selectionPayload string,
	backend nvcastorage.HelmCacheBackend,
) error {
	if existing == nil {
		return fmt.Errorf("existing model cache StorageRequest is nil")
	}
	conflict := func(reason string) error {
		return &storageSelectionConflictError{namespace: existing.Namespace, name: existing.Name, reason: reason}
	}
	if existing.Name != nvcav2beta1.ModelCacheRequest.Name() ||
		existing.Spec.Type != nvcav2beta1.ModelCacheRequest {
		return conflict("name or type does not match")
	}
	if backend != nvcastorage.HelmCacheBackendNVMesh {
		return conflict(fmt.Sprintf("backend %q does not create a StorageRequest", backend))
	}
	if existing.Spec.ModelCache == nil {
		return conflict("modelCache spec is missing")
	}
	if existing.Spec.RequestName != icmsReq.Name || existing.Spec.RequestNamespace != icmsReq.Namespace {
		return conflict("request identity does not match")
	}
	if existing.Annotations[nvcastorage.ICMSRequestUIDAnnotationKey] != string(icmsReq.UID) {
		return conflict("request UID does not match")
	}
	if existing.Spec.ModelCache.CacheHandle != helmModelCacheHandle(icmsReq) {
		return conflict("cache handle does not match")
	}
	if existing.Spec.ModelCache.Backend != string(backend) {
		return conflict("backend does not match")
	}
	if existing.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] != selectionPayload {
		return conflict("storage selection does not match")
	}
	return nil
}

func helmModelCacheHandle(req *nvcav2beta1.ICMSRequest) string {
	switch {
	case req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil &&
		req.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification != nil:
		return req.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification.CacheHandle
	case req.Spec.CreationMsgInfo.TaskLaunchSpecification != nil &&
		req.Spec.CreationMsgInfo.TaskLaunchSpecification.CacheLaunchSpecification != nil:
		return req.Spec.CreationMsgInfo.TaskLaunchSpecification.CacheLaunchSpecification.CacheHandle
	default:
		return ""
	}
}

func persistedDurableHelmCacheSelection(req *nvcav2beta1.ICMSRequest) (bool, error) {
	raw := req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
	if raw == "" {
		return false, nil
	}
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(raw)
	if err != nil {
		return false, fmt.Errorf("parse persisted model cache storage selection: %w", err)
	}
	if selection.Workflow != nvcastorage.ModelCacheWorkflowHelm {
		return false, fmt.Errorf("persisted model cache workflow %q is not Helm", selection.Workflow)
	}
	return selection.Mode == nvcastorage.ModelCacheSelectionDurable, nil
}

// storageSelectionConflictError reports an existing model cache StorageRequest
// that does not match the selection persisted on its request.
type storageSelectionConflictError struct {
	namespace, name, reason string
}

func (e *storageSelectionConflictError) Error() string {
	return fmt.Sprintf("existing model cache StorageRequest %s/%s conflicts with persisted selection: %s",
		e.namespace, e.name, e.reason)
}

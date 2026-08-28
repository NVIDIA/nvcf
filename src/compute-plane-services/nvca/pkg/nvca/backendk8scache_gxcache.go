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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sapitypes "k8s.io/apimachinery/pkg/types"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type k8sNamespacePatcher interface {
	Patch(
		ctx context.Context,
		name string,
		pt k8sapitypes.PatchType,
		data []byte,
		opts metav1.PatchOptions,
		subresources ...string) (result *corev1.Namespace, err error)
}

func ensureGXCacheNamespaceLabels(ctx context.Context, nsPatcher k8sNamespacePatcher, namespace string) error {
	// ~1 replacement is needed to escape the forward slash for JSON patch
	patchData := []byte(fmt.Sprintf(`[{"op": "replace", "path": "/metadata/labels/%s", "value": "true"}]`,
		strings.ReplaceAll(nvcatypes.ShaderCacheLabelKey, "/", "~1")))
	_, err := nsPatcher.Patch(ctx, namespace, k8sapitypes.JSONPatchType, patchData, metav1.PatchOptions{})
	return err
}

// ensureModelCacheNamespaceLabel patches WorkloadInstanceTypeLabel onto the
// model-cache init namespace so the Kyverno add-unbound-dns policy injects
// the nvcf-unbound nameserver into writer job pods. Called at NVCA startup so
// the label is applied immediately on upgrade, before any model cache reconcile
// runs. JSON patch "add" is idempotent: it inserts the key when absent and
// updates it when present, so re-running on an already-labelled namespace is safe.
func ensureModelCacheNamespaceLabel(ctx context.Context, nsPatcher k8sNamespacePatcher, namespace string) error {
	key := strings.ReplaceAll(nvcatypes.WorkloadInstanceTypeLabel, "/", "~1")
	patchData := []byte(fmt.Sprintf(`[{"op": "add", "path": "/metadata/labels/%s", "value": %q}]`,
		key, nvcatypes.WorkloadInstanceTypeValueMiniService))
	_, err := nsPatcher.Patch(ctx, namespace, k8sapitypes.JSONPatchType, patchData, metav1.PatchOptions{})
	return err
}

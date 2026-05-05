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

package mscontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

const (
	revisionConfigMapPrefix = "miniservice-revision-v"
	revisionLabel           = "nvca.nvcf.nvidia.io/revision"
	managedByValue          = "miniservice-controller"

	revisionDataKeyValues     = "values"
	revisionDataKeyRenderHash = "renderHash"
	revisionDataKeyChartURL   = "chartURL"
	revisionDataKeyTimestamp  = "timestamp"
)

// saveRevisionHistory persists the applied helm values and render details
// as a ConfigMap in the MiniService's managed namespace after a successful install or upgrade.
// Each ConfigMap is named after the current revision and stores the values that were applied.
func (r *Reconciler) saveRevisionHistory(ctx context.Context, ms *v1alpha1.MiniService) error {
	if !r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.MiniServiceRevisionHistory) {
		return nil
	}

	log := logf.FromContext(ctx)

	revision := ms.Status.Revision
	cmName := fmt.Sprintf("%s%d", revisionConfigMapPrefix, revision)

	// If the revision ConfigMap already exists, do nothing.
	cmKey := client.ObjectKey{Namespace: ms.Spec.Namespace, Name: cmName}
	if err := r.Client.Get(ctx, cmKey, &corev1.ConfigMap{}); err == nil {
		log.V(1).Info("Revision ConfigMap already exists, skipping save", "revision", revision, "configmap", cmName)
		return nil
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ms.Spec.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": managedByValue,
				miniserviceNameLabel:           ms.Name,
				revisionLabel:                  strconv.FormatInt(revision, 10),
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
				Kind:       "MiniService",
				Name:       ms.Name,
				UID:        ms.UID,
			}},
		},
		Data: map[string]string{
			revisionDataKeyValues:    string(ms.Spec.HelmChartConfig.Values),
			revisionDataKeyChartURL:  ms.Spec.HelmChartConfig.URL,
			revisionDataKeyTimestamp: r.now().UTC().Format(time.RFC3339Nano),
		},
	}
	if ms.Status.RenderDetails != nil {
		cm.Data[revisionDataKeyRenderHash] = ms.Status.RenderDetails.Hash
	}

	log.Info("Saving revision history", "revision", revision, "configmap", cm.Name)
	if err := r.Client.Create(ctx, cm); err != nil {
		return fmt.Errorf("create revision configmap %s: %w", cm.Name, err)
	}
	return nil
}

// helmValuesChanged compares the current spec's helm values against the values
// stored in the latest revision ConfigMap. Returns true if values differ or if
// no revision history exists (conservative: assume changed when unknown).
func (r *Reconciler) helmValuesChanged(ctx context.Context, ms *v1alpha1.MiniService) (bool, error) {
	cmList, err := r.listRevisionHistory(ctx, ms)
	if err != nil {
		return false, err
	}
	if len(cmList.Items) == 0 {
		return true, nil
	}

	latest := latestRevisionConfigMap(cmList.Items)
	if latest == nil {
		return true, nil
	}
	storedValues := latest.Data[revisionDataKeyValues]
	changed, err := jsonValuesChanged(ms.Spec.HelmChartConfig.Values, []byte(storedValues))
	if err != nil {
		return false, fmt.Errorf("compare old and new helm values: %w", err)
	}
	return changed, nil
}

// jsonValuesChanged returns true when the two JSON blobs represent different objects.
func jsonValuesChanged(a, b json.RawMessage) (bool, error) {
	var aObj, bObj any
	if err := json.Unmarshal(a, &aObj); err != nil {
		return false, fmt.Errorf("unmarshal a: %w", err)
	}
	if err := json.Unmarshal(b, &bObj); err != nil {
		return false, fmt.Errorf("unmarshal b: %w", err)
	}
	return !cmp.Equal(aObj, bObj), nil
}

// latestRevisionConfigMap returns the ConfigMap with the highest revision label value.
func latestRevisionConfigMap(cms []corev1.ConfigMap) *corev1.ConfigMap {
	var best *corev1.ConfigMap
	bestRev := int64(-1)
	for i := range cms {
		rev, err := strconv.ParseInt(cms[i].Labels[revisionLabel], 10, 64)
		if err != nil {
			continue
		}
		if rev > bestRev {
			bestRev = rev
			best = &cms[i]
		}
	}
	return best
}

// listRevisionHistory returns all revision ConfigMaps for a MiniService.
func (r *Reconciler) listRevisionHistory(ctx context.Context, ms *v1alpha1.MiniService) (*corev1.ConfigMapList, error) {
	cmList := &corev1.ConfigMapList{}
	if err := r.Client.List(ctx, cmList,
		client.InNamespace(ms.Spec.Namespace),
		client.MatchingLabels{
			"app.kubernetes.io/managed-by": managedByValue,
			miniserviceNameLabel:           ms.Name,
		},
	); err != nil {
		return nil, fmt.Errorf("list revision configmaps: %w", err)
	}
	return cmList, nil
}

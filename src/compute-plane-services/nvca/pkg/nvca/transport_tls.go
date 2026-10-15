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

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
)

func (c K8sComputeBackend) prepareTransportTLSForPod(ctx context.Context, pod *corev1.Pod) error {
	if c.bk8s.cfg.Workload.TransportTLS == nil {
		return nil
	}
	cfg := transporttls.NormalizeConfig(*c.bk8s.cfg.Workload.TransportTLS)
	if cfg.TrustMode != transporttls.TrustModeBundle || !transporttls.PodSpecHasLLMWorker(&pod.Spec) {
		return nil
	}
	if err := transporttls.ValidateConfig(cfg); err != nil {
		return nvcaerrors.TerminalError(err)
	}
	if err := c.ensureTransportTLSConfigMap(ctx, pod.Namespace, cfg); err != nil {
		return err
	}
	if err := transporttls.InjectIntoPodSpec(&pod.Spec, cfg); err != nil {
		return nvcaerrors.TerminalError(err)
	}
	return nil
}

func (c K8sComputeBackend) ensureTransportTLSConfigMap(
	ctx context.Context,
	namespace string,
	cfg nvcaconfig.TransportTLSConfig,
) error {
	desiredData := transporttls.DesiredConfigMapData(cfg)
	cmClient := c.clients.K8s.CoreV1().ConfigMaps(namespace)
	existing, err := cmClient.Get(ctx, cfg.TrustBundleConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cmClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cfg.TrustBundleConfigMapName,
				Namespace: namespace,
			},
			Data: desiredData,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}

	if existing.Data == nil {
		existing.Data = map[string]string{}
	}
	changed := false
	for key, value := range desiredData {
		if existing.Data[key] != value {
			existing.Data[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err = cmClient.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

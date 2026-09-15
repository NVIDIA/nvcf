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

package kata

import (
	"context"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ComponentName for health checks.
	ComponentName = "kata"
)

var (
	// RuntimeClassNameGPU to apply to all Pod specs with GPUs when KataRuntimeIsolation is enabled.
	RuntimeClassNameGPU = "kata-qemu-nvidia-gpu"
	// RuntimeClassNameNonGPU is a separate rt class for Non-GPU pods that defaults to less VM memory overhead.
	RuntimeClassNameNonGPU = "kata-qemu"
)

func init() {
	if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS"); rtcNameOverride != "" {
		RuntimeClassNameGPU = rtcNameOverride
	} else if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS_GPU"); rtcNameOverride != "" {
		RuntimeClassNameGPU = rtcNameOverride
	}
	if rtcNameOverride := os.Getenv("NVCA_KATA_RUNTIME_CLASS_NON_GPU"); rtcNameOverride != "" {
		RuntimeClassNameNonGPU = rtcNameOverride
	}
}

// TODO(estroczynski): query DCGM metrics from prometheus for all node's health.
func NewStatusGetter(k8sclient kubernetes.Interface) health.ComponentStatusGetter {
	return health.GetComponentStatusFunc(func(ctx context.Context) (hs types.AgentHealth, err error) {
		ch := types.ComponentHealth{
			Status:      types.HealthStatusHealthy,
			StatusLevel: types.StatusLevelWarn,
		}
		// TODO: since not all clusters have the non-GPU rt class, skip health checking for now.
		// Once the change is merged into kata-deploy and clusters are upgraded,
		// that rt class should be checked here.
		_, err = k8sclient.NodeV1().RuntimeClasses().Get(ctx, RuntimeClassNameGPU, metav1.GetOptions{})

		// Track K8s API call metrics
		if m := metrics.FromContext(ctx); m != nil {
			m.TrackK8sAPICall("runtimeclass", err)
		}

		if err != nil {
			ch.Status = types.HealthStatusUnhealthy
			ch.Errors = append(ch.Errors, err.Error())
			ch.StatusLevel = types.StatusLevelError
		}
		hs.Components = map[string]types.ComponentHealth{
			ComponentName: ch,
		}
		return hs, nil
	})
}

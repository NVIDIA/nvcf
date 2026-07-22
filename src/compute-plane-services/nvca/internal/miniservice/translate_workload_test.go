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
	"encoding/base64"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestTranslateWorkloadLLMUsesCanonicalPylonArgs(t *testing.T) {
	reconciler := &Reconciler{
		ControllerOptions: ControllerOptions{FeatureFlagFetcher: &featureflagmock.Fetcher{}},
	}
	miniService := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "mini-service"},
		Spec:       nvcav1alpha1.MiniServiceSpec{Namespace: "mini-service-workload"},
	}
	icmsRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "mini-service-request"},
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.FunctionCreationAction,
			FunctionDetails: function.Details{
				FunctionID:        "function-id",
				FunctionVersionID: "function-version-id",
				FunctionType:      function.FunctionTypeLLM,
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					Action:            common.FunctionCreationAction,
					RequestID:         "request-id",
					MessageBatchID:    "batch-id",
					NCAID:             "nca-id",
					InstanceTypeName:  "H100_1x",
					InstanceTypeValue: "H100",
					GPUType:           "H100",
					RequestedGPUCount: 1,
					InstanceCount:     1,
				},
				FunctionLaunchSpecification: &function.LaunchSpecification{
					EnvironmentB64: base64.StdEncoding.EncodeToString([]byte(strings.Join([]string{
						common.UtilsImageEnv + "=example.test/utils:latest",
						common.InitImageEnv + "=example.test/init:latest",
						"INFERENCE_PORT=8000",
						"LLM_REQUEST_ROUTER_ADDRESS=router.example.test:443",
						"HELM_CHART_INFERENCE_SERVICE_NAME=inference-service",
					}, "\n"))),
					ICMSEnvironment: "test",
					CloudProvider:   "ON_PREM",
					HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
						HelmChartURL: "https://charts.example.test/sample.tgz",
					},
				},
			},
		},
	}

	objects, err := reconciler.translateWorkload(miniService, icmsRequest)
	require.NoError(t, err)

	var pod *corev1.Pod
	for _, object := range objects {
		if candidate, ok := object.(*corev1.Pod); ok {
			pod = candidate
			break
		}
	}
	require.NotNil(t, pod)

	var llmWorker *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == function.LLMWorkerContainerName {
			llmWorker = &pod.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, llmWorker)
	var backendConnectivityArgs, initialInputTPSArgs []string
	for _, arg := range llmWorker.Args {
		if strings.HasPrefix(arg, "--backend-connectivity=") {
			backendConnectivityArgs = append(backendConnectivityArgs, arg)
		}
		if strings.HasPrefix(arg, "--initial-input-tps=") {
			initialInputTPSArgs = append(initialInputTPSArgs, arg)
		}
		assert.False(t, strings.HasPrefix(arg, "--reverse-tunnel"))
		assert.False(t, strings.HasPrefix(arg, "--do-calibration"))
	}
	assert.Equal(t, []string{"--backend-connectivity=reverse"}, backendConnectivityArgs)
	assert.Equal(t, []string{"--initial-input-tps=100"}, initialInputTPSArgs)
}

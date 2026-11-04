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

package function

import (
	"os"

	corev1 "k8s.io/api/core/v1"
)

const (
	NVCFSBSZoneDNSEnv         = "NVCF_SBS_ZONE_DNS"
	NVCFStreamingInterfaceEnv = "NVCF_LLS_INTERFACE"

	// Default values
	defaultZoneDNS            = "http://nvcf-sbs.nvcf-backend.svc.cluster.local:8000"
	defaultStreamingInterface = "ALL"
)

func getLLSEnvSet() []corev1.EnvVar {
	zoneDNS := os.Getenv(NVCFSBSZoneDNSEnv)
	if zoneDNS == "" {
		zoneDNS = defaultZoneDNS
	}
	streamingInterface := os.Getenv(NVCFStreamingInterfaceEnv)
	if streamingInterface == "" {
		streamingInterface = defaultStreamingInterface
	}

	return []corev1.EnvVar{
		{
			Name: "NVCF_WORKER_NODE_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.hostIP",
				},
			},
		},
		{
			Name:  "ZONE_DNS",
			Value: zoneDNS,
		},
		{
			Name:  "STREAMING_INTERFACE",
			Value: streamingInterface,
		},
	}
}

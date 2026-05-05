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

package kubeclients

import (
	kaischedulingv2 "github.com/NVIDIA/KAI-scheduler/pkg/apis/scheduling/v2"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	bartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
)

type KubeClients struct {
	Config          *rest.Config
	DiscoveryClient discovery.DiscoveryInterface
	K8s             k8sclient.Interface
	BART            bartclient.Interface
	HelmV2          client.WithWatch
}

func NewFromCore(coreK8sClient *core.KubeClients) (*KubeClients, error) {
	bc, err := bartclient.NewForConfig(coreK8sClient.Config)
	if err != nil {
		return nil, err
	}

	dc, err := discovery.NewDiscoveryClientForConfig(coreK8sClient.Config)
	if err != nil {
		return nil, err
	}

	schemeHelmV2 := runtime.NewScheme()
	utilruntime.Must(v1alpha1.AddToScheme(schemeHelmV2))
	utilruntime.Must(kaischedulingv2.AddToScheme(schemeHelmV2))
	helmV2Client, err := client.NewWithWatch(coreK8sClient.Config, client.Options{
		Scheme: schemeHelmV2,
	})
	if err != nil {
		return nil, err
	}

	return &KubeClients{
		Config:          coreK8sClient.Config,
		K8s:             coreK8sClient.K8s,
		BART:            bc,
		HelmV2:          helmV2Client,
		DiscoveryClient: dc,
	}, nil
}

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
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	apiextnv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	nvcaopclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
)

type KubeClients struct {
	Config              *rest.Config
	K8s                 k8sclient.Interface
	APIExtV1            apiextnv1client.ApiextensionsV1Interface
	NVCAOP              nvcaopclient.Interface
	DynamicClient       dynamic.Interface
	DiscoveryClient     discovery.DiscoveryInterface
	DiscoveryRESTMapper meta.RESTMapper
}

func NewFromCore(coreK8sClient *core.KubeClients, dynClient dynamic.Interface, discClient discovery.DiscoveryInterface) (*KubeClients, error) {
	nvcaop, err := nvcaopclient.NewForConfig(coreK8sClient.Config)
	if err != nil {
		return nil, err
	}

	dClient, err := apiextnv1client.NewForConfig(coreK8sClient.Config)
	if err != nil {
		return nil, err
	}

	grs, err := restmapper.GetAPIGroupResources(discClient)
	if err != nil {
		return nil, err
	}

	return &KubeClients{
		Config:              coreK8sClient.Config,
		K8s:                 coreK8sClient.K8s,
		APIExtV1:            dClient,
		NVCAOP:              nvcaop,
		DynamicClient:       dynClient,
		DiscoveryClient:     discClient,
		DiscoveryRESTMapper: restmapper.NewDiscoveryRESTMapper(grs),
	}, nil
}

func (c *KubeClients) GetDynamicResourceClient(gvk schema.GroupVersionKind) (dynamic.NamespaceableResourceInterface, error) {
	rm, err := c.DiscoveryRESTMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("get REST mapping for gvk: %w", err)
	}

	return c.DynamicClient.Resource(rm.Resource), nil
}
